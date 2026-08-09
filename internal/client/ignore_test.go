package client

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestOutboundIgnoreHonorsNestedGitignore(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}

	repo := t.TempDir()
	if err := runGit(repo, "init"); err != nil {
		t.Fatalf("git init failed: %v", err)
	}

	if err := os.MkdirAll(filepath.Join(repo, "app"), 0755); err != nil {
		t.Fatalf("failed to create app directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("build/\n"), 0644); err != nil {
		t.Fatalf("failed to write root .gitignore: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "app", ".gitignore"), []byte("*.log\n!keep.log\n"), 0644); err != nil {
		t.Fatalf("failed to write nested .gitignore: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "app", "error.log"), []byte("x"), 0644); err != nil {
		t.Fatalf("failed to write ignored log file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "app", "keep.log"), []byte("x"), 0644); err != nil {
		t.Fatalf("failed to write unignored log file: %v", err)
	}

	ignore := NewOutboundIgnore(filepath.Join(repo, "app"))
	if ignore.git == nil {
		t.Fatalf("expected git matcher to be enabled")
	}

	if !ignore.Match("error.log", false) {
		t.Fatalf("expected app/error.log to be ignored by nested .gitignore")
	}
	if ignore.Match("keep.log", false) {
		t.Fatalf("expected app/keep.log to be unignored by nested .gitignore")
	}

	ignore.git.processMu.Lock()
	firstPID := ignore.git.process.Process.Pid
	ignore.git.processMu.Unlock()
	if ignore.Match("file with spaces.txt", false) {
		t.Fatal("ordinary filename with spaces was ignored")
	}
	ignore.git.processMu.Lock()
	secondPID := ignore.git.process.Process.Pid
	ignore.git.processMu.Unlock()
	if secondPID != firstPID {
		t.Fatalf("git check-ignore process was not reused: %d then %d", firstPID, secondPID)
	}

	if err := os.WriteFile(filepath.Join(repo, "app", ".gitignore"), []byte("*.log\n!keep.log\nsecret.txt\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ignore.Invalidate()
	if !ignore.Match("secret.txt", false) {
		t.Fatal("updated .gitignore policy was not loaded")
	}

	ignore.git.processMu.Lock()
	_ = ignore.git.process.Process.Kill()
	ignore.git.processMu.Unlock()
	if ignore.Match("after-process-failure.txt", false) {
		t.Fatal("ordinary file was ignored after git process restart")
	}
	ignore.Close()
	ignore.git.processMu.Lock()
	process := ignore.git.process
	ignore.git.processMu.Unlock()
	if process != nil {
		t.Fatal("git check-ignore process remained open")
	}
}

func runGit(dir string, args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	return cmd.Run()
}
