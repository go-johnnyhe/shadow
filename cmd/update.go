package cmd

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/go-johnnyhe/shadow/internal/ui"
	"github.com/spf13/cobra"
)

var (
	updateMetadataHTTPClient = &http.Client{Timeout: 15 * time.Second}
	updateDownloadHTTPClient = &http.Client{Timeout: 2 * time.Minute}
)

const maxReleaseArchiveBytes = 100 * 1024 * 1024

var updateCmd = &cobra.Command{
	Use:   "update",
	Short: "Update shadow to the latest release",
	Run: func(cmd *cobra.Command, args []string) {
		if err := runUpdate(); err != nil {
			fmt.Printf("Error: %v\n", err)
			os.Exit(1)
		}
	},
}

func init() {
	rootCmd.AddCommand(updateCmd)
}

type githubRelease struct {
	TagName string `json:"tag_name"`
}

func runUpdate() error {
	fmt.Printf("  %s\n", ui.Dim("checking for updates..."))

	resp, err := updateMetadataHTTPClient.Get("https://api.github.com/repos/go-johnnyhe/shadow/releases/latest")
	if err != nil {
		return fmt.Errorf("failed to check for updates: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GitHub API returned status %s", resp.Status)
	}

	var release githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return fmt.Errorf("failed to parse release info: %v", err)
	}

	latest := strings.TrimPrefix(release.TagName, "v")
	current := strings.TrimPrefix(Version, "v")

	if current != "dev" && current == latest {
		fmt.Printf("  %s\n", ui.Accent("already up to date (v"+latest+")"))
		return nil
	}

	if current == "dev" {
		fmt.Printf("  %s\n", ui.Dim("dev build detected, updating to v"+latest))
	} else {
		fmt.Printf("  %s\n", ui.Dim("updating v"+current+" → v"+latest))
	}

	execPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to determine executable path: %v", err)
	}

	assetName := fmt.Sprintf("shadow_%s_%s_%s.tar.gz", latest, runtime.GOOS, runtime.GOARCH)
	releaseBaseURL := fmt.Sprintf("https://github.com/go-johnnyhe/shadow/releases/download/v%s", latest)
	archivePath, err := downloadVerifiedReleaseArchive(
		releaseBaseURL+"/"+assetName,
		releaseBaseURL+"/checksums.txt",
		assetName,
		filepath.Dir(execPath),
	)
	if err != nil {
		return err
	}
	defer os.Remove(archivePath)

	tmpFile, err := os.CreateTemp(filepath.Dir(execPath), ".shadow-update-binary-*")
	if err != nil {
		return fmt.Errorf("failed to create temporary binary: %v", err)
	}
	tmpPath := tmpFile.Name()
	if err := tmpFile.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	defer os.Remove(tmpPath)

	archive, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("failed to open verified release: %v", err)
	}
	extractErr := extractShadowFromTgz(archive, tmpPath)
	_ = archive.Close()
	if extractErr != nil {
		return fmt.Errorf("failed to extract binary: %v", extractErr)
	}

	if err := os.Chmod(tmpPath, 0755); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("failed to set permissions: %v", err)
	}

	if err := os.Rename(tmpPath, execPath); err != nil {
		os.Remove(tmpPath)
		if os.IsPermission(err) {
			return fmt.Errorf("permission denied — try running with sudo")
		}
		return fmt.Errorf("failed to replace binary: %v", err)
	}

	fmt.Printf("  %s\n", ui.Accent("updated to v"+latest))

	// Auto-configure MCP for AI agents if any are detected.
	runMCPInstall()

	return nil
}

func downloadVerifiedReleaseArchive(archiveURL, checksumURL, assetName, directory string) (string, error) {
	checksumResp, err := updateDownloadHTTPClient.Get(checksumURL)
	if err != nil {
		return "", fmt.Errorf("failed to download release checksums: %v", err)
	}
	if checksumResp.StatusCode != http.StatusOK {
		_ = checksumResp.Body.Close()
		return "", fmt.Errorf("checksum download failed with status %s", checksumResp.Status)
	}
	checksumData, readErr := io.ReadAll(io.LimitReader(checksumResp.Body, 1024*1024+1))
	_ = checksumResp.Body.Close()
	if readErr != nil {
		return "", fmt.Errorf("failed to read release checksums: %v", readErr)
	}
	if len(checksumData) > 1024*1024 {
		return "", fmt.Errorf("release checksum file exceeds size limit")
	}
	expected, err := checksumForAsset(checksumData, assetName)
	if err != nil {
		return "", err
	}

	archiveResp, err := updateDownloadHTTPClient.Get(archiveURL)
	if err != nil {
		return "", fmt.Errorf("failed to download release: %v", err)
	}
	defer archiveResp.Body.Close()
	if archiveResp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download failed with status %s", archiveResp.Status)
	}

	temporary, err := os.CreateTemp(directory, ".shadow-release-*.tar.gz")
	if err != nil {
		return "", fmt.Errorf("failed to create temporary archive: %v", err)
	}
	temporaryPath := temporary.Name()
	keep := false
	defer func() {
		if !keep {
			_ = os.Remove(temporaryPath)
		}
	}()
	hasher := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(temporary, hasher), io.LimitReader(archiveResp.Body, maxReleaseArchiveBytes+1))
	closeErr := temporary.Close()
	if copyErr != nil {
		return "", fmt.Errorf("failed to save release: %v", copyErr)
	}
	if closeErr != nil {
		return "", fmt.Errorf("failed to close release archive: %v", closeErr)
	}
	if written > maxReleaseArchiveBytes {
		return "", fmt.Errorf("release archive exceeds size limit")
	}
	if !strings.EqualFold(hex.EncodeToString(hasher.Sum(nil)), expected) {
		return "", fmt.Errorf("release checksum verification failed")
	}
	keep = true
	return temporaryPath, nil
}

func checksumForAsset(checksums []byte, assetName string) (string, error) {
	for _, line := range strings.Split(string(checksums), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || fields[1] != assetName {
			continue
		}
		digest, err := hex.DecodeString(fields[0])
		if err != nil || len(digest) != sha256.Size {
			return "", fmt.Errorf("invalid checksum for %s", assetName)
		}
		return fields[0], nil
	}
	return "", fmt.Errorf("release checksum is missing %s", assetName)
}

func extractShadowFromTgz(reader io.Reader, outputPath string) error {
	gzReader, err := gzip.NewReader(reader)
	if err != nil {
		return fmt.Errorf("failed to create gzip reader: %v", err)
	}
	defer gzReader.Close()

	tarReader := tar.NewReader(gzReader)
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("error reading tar: %v", err)
		}

		binaryName := path.Base(header.Name)
		if header.Typeflag == tar.TypeReg && (binaryName == "shadow" || binaryName == "shadow.exe") {
			if header.Size < 0 || header.Size > maxReleaseArchiveBytes {
				return fmt.Errorf("shadow binary exceeds size limit")
			}
			outFile, err := os.Create(outputPath)
			if err != nil {
				return fmt.Errorf("failed to create output file: %v", err)
			}
			defer outFile.Close()

			if _, err := io.Copy(outFile, tarReader); err != nil {
				return fmt.Errorf("failed to write binary: %v", err)
			}
			return nil
		}
	}
	return fmt.Errorf("shadow binary not found in archive")
}
