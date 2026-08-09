package client

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"log"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

var hardcodedIgnore = regexp.MustCompile(`(?i)` +
	// Directories: VCS, editors, caches, system, secrets
	`(?:^|[\\/])(?:\.git|\.hg|\.svn|\.vscode|\.idea|\.opencode|node_modules|` +
	`__pycache__|\.mypy_cache|\.pytest_cache|\.ruff_cache|` +
	`\.cache|\.local|\.ssh|\.gnupg|\.aws|\.shadow|\.shadow-conflicts|\.Trash)(?:[\\/]|$)` +
	// Shell history, config, and completion files
	`|(?:^|[\\/])\.(?:bash_history|zsh_history|sh_history|python_history|node_repl_history|lesshst|wget-hsts)(?:\.LOCK)?$` +
	`|(?:^|[\\/])\.(?:bashrc|zshrc|profile|bash_profile|zprofile|bash_logout|zlogout)$` +
	`|(?:^|[\\/])\.zcompdump` +
	// PostgreSQL temp, macOS, vim swap, temp files
	`|(?:^|[\\/])\.s\.pgsql\.\d+$` +
	`|\.ds_store$|\.sw[a-p0-9]$|\.swp$|\.swo$|~$|\.bak$|\.tmp$`)

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

func (o *OutboundIgnore) Close() {
	if o != nil && o.git != nil {
		o.git.Close()
	}
}

func (o *OutboundIgnore) Invalidate() {
	if o != nil && o.git != nil {
		o.git.Invalidate()
	}
}

func shouldIgnoreInbound(relPath string) bool {
	return hardcodedIgnore.MatchString(relPath)
}

type gitIgnoreMatcher struct {
	baseDir       string
	gitRoot       string
	processMu     sync.Mutex
	process       *exec.Cmd
	processInput  io.WriteCloser
	processOutput *bufio.Reader
	processStart  time.Time
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

	return &gitIgnoreMatcher{baseDir: normalizedBaseDir, gitRoot: gitRoot}, nil
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

func (m *gitIgnoreMatcher) Match(relPath string, isDir bool) bool {
	repoRelPath, ok := m.repoRelativePath(relPath)
	if !ok {
		return false
	}

	ignored, known := m.matchWithGit(repoRelPath, isDir)
	return known && ignored
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

func (m *gitIgnoreMatcher) matchWithGit(repoRelPath string, asDir bool) (bool, bool) {
	candidate := repoRelPath
	if asDir {
		candidate = strings.TrimSuffix(candidate, "/") + "/"
	}

	m.processMu.Lock()
	defer m.processMu.Unlock()
	for attempt := 0; attempt < 2; attempt++ {
		ignored, err := m.queryGitLocked(candidate)
		if err == nil {
			return ignored, true
		}
		m.stopGitLocked()
	}
	return false, false
}

func (m *gitIgnoreMatcher) queryGitLocked(candidate string) (bool, error) {
	if m.process != nil && time.Since(m.processStart) >= time.Second {
		m.stopGitLocked()
	}
	if m.process == nil {
		cmd := exec.Command("git", "-C", m.gitRoot, "check-ignore", "--stdin", "-z", "--verbose", "--non-matching")
		input, err := cmd.StdinPipe()
		if err != nil {
			return false, err
		}
		output, err := cmd.StdoutPipe()
		if err != nil {
			_ = input.Close()
			return false, err
		}
		cmd.Stderr = io.Discard
		if err := cmd.Start(); err != nil {
			_ = input.Close()
			return false, err
		}
		m.process = cmd
		m.processInput = input
		m.processOutput = bufio.NewReader(output)
		m.processStart = time.Now()
	}

	if _, err := io.WriteString(m.processInput, candidate+"\x00"); err != nil {
		return false, err
	}
	fields := make([]string, 4)
	for i := range fields {
		field, err := m.processOutput.ReadString('\x00')
		if err != nil {
			return false, err
		}
		fields[i] = strings.TrimSuffix(field, "\x00")
	}
	if fields[3] != candidate {
		return false, fmt.Errorf("git check-ignore returned path %q for %q", fields[3], candidate)
	}
	return fields[2] != "" && !strings.HasPrefix(fields[2], "!"), nil
}

func (m *gitIgnoreMatcher) stopGitLocked() {
	if m.processInput != nil {
		_ = m.processInput.Close()
	}
	if m.process != nil && m.process.Process != nil {
		_ = m.process.Process.Kill()
		_ = m.process.Wait()
	}
	m.process = nil
	m.processInput = nil
	m.processOutput = nil
	m.processStart = time.Time{}
}

func (m *gitIgnoreMatcher) Close() {
	m.processMu.Lock()
	m.stopGitLocked()
	m.processMu.Unlock()
}

func (m *gitIgnoreMatcher) Invalidate() {
	m.processMu.Lock()
	m.stopGitLocked()
	m.processMu.Unlock()
}
