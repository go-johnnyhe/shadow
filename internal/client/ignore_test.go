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
}

func runGit(dir string, args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	return cmd.Run()
}
