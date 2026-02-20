package client

import (
	"errors"
	"log"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

var hardcodedIgnore = regexp.MustCompile(`(?i)(?:^|[\\/])(?:\.git|\.hg|\.svn|\.vscode|\.idea|node_modules|\.opencode)(?:[\\/]|$)|(?:^|[\\/])\.s\.pgsql\.\d+$|\.ds_store$|\.sw[a-p0-9]$|\.swp$|\.swo$|~$|\.bak$|\.tmp$`)

type OutboundIgnore struct {
	git *gitIgnoreMatcher
}

func NewOutboundIgnore(baseDir string) *OutboundIgnore {
	outbound := &OutboundIgnore{}
	gitMatcher, err := newGitIgnoreMatcher(baseDir)
	if err != nil {
		log.Printf("gitignore support disabled for %s: %v", baseDir, err)
	}
	outbound.git = gitMatcher
	return outbound
}

func (o *OutboundIgnore) Match(relPath string, isDir bool) bool {
	if hardcodedIgnore.MatchString(relPath) {
		return true
	}
	if o == nil || o.git == nil {
		return false
	}
	return o.git.Match(relPath, isDir)
}

func shouldIgnoreInbound(relPath string) bool {
	return hardcodedIgnore.MatchString(relPath)
}

type gitIgnoreMatcher struct {
	baseDir         string
	gitRoot         string
	mu              sync.RWMutex
	ignoredFiles    map[string]struct{}
	ignoredDirs     map[string]struct{}
	notIgnoredFiles map[string]struct{}
	notIgnoredDirs  map[string]struct{}
}

func newGitIgnoreMatcher(baseDir string) (*gitIgnoreMatcher, error) {
	normalizedBaseDir, err := normalizePath(baseDir)
	if err != nil {
		return nil, err
	}

	gitRoot, err := gitRepositoryRoot(normalizedBaseDir)
	if err != nil || gitRoot == "" {
		return nil, err
	}

	matcher := &gitIgnoreMatcher{
		baseDir:         normalizedBaseDir,
		gitRoot:         gitRoot,
		ignoredFiles:    map[string]struct{}{},
		ignoredDirs:     map[string]struct{}{},
		notIgnoredFiles: map[string]struct{}{},
		notIgnoredDirs:  map[string]struct{}{},
	}
	if err := matcher.preloadIgnoredPaths(); err != nil {
		return nil, err
	}
	return matcher, nil
}

func gitRepositoryRoot(baseDir string) (string, error) {
	cmd := exec.Command("git", "-C", baseDir, "rev-parse", "--show-toplevel")
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return "", nil
		}
		return "", nil
	}
	root := strings.TrimSpace(string(out))
	if root == "" {
		return "", nil
	}
	return normalizePath(root)
}

func (m *gitIgnoreMatcher) preloadIgnoredPaths() error {
	cmd := exec.Command("git", "-C", m.gitRoot, "ls-files", "--others", "-i", "--exclude-standard", "--directory", "-z")
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return nil
		}
		return err
	}

	for _, entry := range strings.Split(string(out), "\x00") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		m.cacheIgnoredRepoPath(entry, strings.HasSuffix(entry, "/"))
	}
	return nil
}

func (m *gitIgnoreMatcher) Match(relPath string, isDir bool) bool {
	repoRelPath, ok := m.repoRelativePath(relPath)
	if !ok {
		return false
	}

	if m.matchesCache(repoRelPath, isDir) {
		return true
	}
	if m.isCachedNotIgnored(repoRelPath, isDir) {
		return false
	}

	ignored, known := m.matchWithGit(repoRelPath, isDir)
	if !known {
		return false
	}
	if ignored {
		m.cacheIgnoredRepoPath(repoRelPath, isDir)
		return true
	}
	m.cacheNotIgnoredRepoPath(repoRelPath, isDir)
	return false
}

func (m *gitIgnoreMatcher) repoRelativePath(relPath string) (string, bool) {
	absPath := filepath.Join(m.baseDir, filepath.FromSlash(relPath))
	absPath = filepath.Clean(absPath)
	repoRel, err := filepath.Rel(m.gitRoot, absPath)
	if err != nil {
		return "", false
	}
	repoRel = path.Clean(filepath.ToSlash(repoRel))
	if repoRel == "." || repoRel == ".." || strings.HasPrefix(repoRel, "../") || strings.HasPrefix(repoRel, "/") {
		return "", false
	}
	return repoRel, true
}

func normalizePath(p string) (string, error) {
	absPath, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(absPath); err == nil {
		return filepath.Clean(resolved), nil
	}
	return filepath.Clean(absPath), nil
}

func (m *gitIgnoreMatcher) matchesCache(repoRelPath string, isDir bool) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if isDir {
		if _, ok := m.ignoredDirs[repoRelPath]; ok {
			return true
		}
	} else {
		if _, ok := m.ignoredFiles[repoRelPath]; ok {
			return true
		}
	}
	if _, ok := m.ignoredDirs[repoRelPath]; ok {
		return true
	}

	current := repoRelPath
	for {
		slash := strings.LastIndex(current, "/")
		if slash == -1 {
			return false
		}
		current = current[:slash]
		if _, ok := m.ignoredDirs[current]; ok {
			return true
		}
	}
}

func (m *gitIgnoreMatcher) isCachedNotIgnored(repoRelPath string, isDir bool) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if isDir {
		_, ok := m.notIgnoredDirs[repoRelPath]
		return ok
	}
	_, ok := m.notIgnoredFiles[repoRelPath]
	return ok
}

func (m *gitIgnoreMatcher) matchWithGit(repoRelPath string, asDir bool) (bool, bool) {
	candidate := repoRelPath
	if asDir {
		candidate = strings.TrimSuffix(candidate, "/") + "/"
	}

	cmd := exec.Command("git", "-C", m.gitRoot, "check-ignore", "-q", "--", candidate)
	err := cmd.Run()
	if err == nil {
		return true, true
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, true
	}
	return false, false
}

func (m *gitIgnoreMatcher) cacheIgnoredRepoPath(repoRelPath string, isDir bool) {
	trimmed := strings.TrimSpace(strings.TrimSuffix(repoRelPath, "/"))
	trimmed = path.Clean(filepath.ToSlash(trimmed))
	if trimmed == "" || trimmed == "." || trimmed == ".." || strings.HasPrefix(trimmed, "../") || strings.HasPrefix(trimmed, "/") {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.notIgnoredFiles, trimmed)
	delete(m.notIgnoredDirs, trimmed)
	if isDir {
		m.ignoredDirs[trimmed] = struct{}{}
		return
	}
	m.ignoredFiles[trimmed] = struct{}{}
}

func (m *gitIgnoreMatcher) cacheNotIgnoredRepoPath(repoRelPath string, isDir bool) {
	trimmed := strings.TrimSpace(strings.TrimSuffix(repoRelPath, "/"))
	trimmed = path.Clean(filepath.ToSlash(trimmed))
	if trimmed == "" || trimmed == "." || trimmed == ".." || strings.HasPrefix(trimmed, "../") || strings.HasPrefix(trimmed, "/") {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if isDir {
		m.notIgnoredDirs[trimmed] = struct{}{}
		return
	}
	m.notIgnoredFiles[trimmed] = struct{}{}
}
