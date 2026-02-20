package cmd

import (
	"bufio"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/go-johnnyhe/shadow/internal/client"
)

const (
	largeShareFileCountThreshold = 500
	largeShareTotalSizeThreshold = 50 * 1024 * 1024
)

type shareSnapshotEstimate struct {
	FileCount  int
	TotalBytes int64
}

func validateShareBaseDir(baseDir string) error {
	blocked := map[string]struct{}{
		"/":        {},
		"/Users":   {},
		"/home":    {},
		"/var":     {},
		"/etc":     {},
		"/usr":     {},
		"/System":  {},
		"/Library": {},
		"/private": {},
		"/opt":     {},
		"/bin":     {},
		"/sbin":    {},
		"/dev":     {},
		"/proc":    {},
		"/sys":     {},
		"/root":    {},
	}

	if home, err := os.UserHomeDir(); err == nil && home != "" {
		blocked[filepath.Clean(home)] = struct{}{}
	}

	candidate := filepath.Clean(baseDir)
	if _, ok := blocked[candidate]; ok {
		return fmt.Errorf("refusing to share %q: choose a project subdirectory instead", candidate)
	}

	if resolved, err := filepath.EvalSymlinks(candidate); err == nil {
		resolved = filepath.Clean(resolved)
		if _, ok := blocked[resolved]; ok {
			return fmt.Errorf("refusing to share %q: choose a project subdirectory instead", resolved)
		}
	}

	return nil
}

func estimateShareSnapshot(baseDir string, ignore *client.OutboundIgnore) (shareSnapshotEstimate, error) {
	estimate := shareSnapshotEstimate{}
	walkErr := filepath.WalkDir(baseDir, func(currentPath string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if currentPath == baseDir {
			return nil
		}

		relPath, err := filepath.Rel(baseDir, currentPath)
		if err != nil {
			return nil
		}
		relPath = path.Clean(filepath.ToSlash(relPath))
		if relPath == "." || relPath == ".." || strings.HasPrefix(relPath, "../") || strings.HasPrefix(relPath, "/") {
			return nil
		}

		if ignore != nil && ignore.Match(relPath, d.IsDir()) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		if d.IsDir() {
			return nil
		}

		info, err := d.Info()
		if err != nil || !info.Mode().IsRegular() {
			return nil
		}

		estimate.FileCount++
		estimate.TotalBytes += info.Size()
		return nil
	})
	if walkErr != nil {
		return estimate, walkErr
	}
	return estimate, nil
}

func isLargeShareEstimate(estimate shareSnapshotEstimate) bool {
	return estimate.FileCount > largeShareFileCountThreshold || estimate.TotalBytes > largeShareTotalSizeThreshold
}

func shouldPromptLargeShare(estimate shareSnapshotEstimate, force bool) bool {
	return isLargeShareEstimate(estimate) && !force
}

func promptLargeShareConfirmation(in io.Reader, out io.Writer, estimate shareSnapshotEstimate) (bool, error) {
	if isNonInteractiveReader(in) {
		return false, fmt.Errorf("large directory detected and no interactive prompt is available; rerun with --force")
	}

	sizeMB := float64(estimate.TotalBytes) / (1024.0 * 1024.0)
	fmt.Fprintf(out, "This directory has ~%d files (~%.1fMB). Shadow is designed for live collaboration on smaller workspaces. Continue anyway? [y/N] ", estimate.FileCount, sizeMB)

	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && err != io.EOF {
		return false, err
	}
	answer := strings.TrimSpace(strings.ToLower(line))
	return answer == "y" || answer == "yes", nil
}

func isNonInteractiveReader(in io.Reader) bool {
	file, ok := in.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice == 0
}
