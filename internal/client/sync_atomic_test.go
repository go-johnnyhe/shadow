package client

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestAtomicWriteFilePreservesExistingMode(t *testing.T) {
	tmpDir := t.TempDir()
	dest := filepath.Join(tmpDir, "script.sh")

	if err := os.WriteFile(dest, []byte("#!/bin/sh\necho old\n"), 0o755); err != nil {
		t.Fatalf("failed to seed file: %v", err)
	}
	if err := os.Chmod(dest, 0o755); err != nil {
		t.Fatalf("failed to set executable bit: %v", err)
	}

	if err := atomicWriteFile(dest, []byte("#!/bin/sh\necho new\n"), 0o644); err != nil {
		t.Fatalf("atomicWriteFile failed: %v", err)
	}

	info, err := os.Stat(dest)
	if err != nil {
		t.Fatalf("failed to stat rewritten file: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o755 {
		t.Fatalf("expected mode 0755 to be preserved, got %04o", got)
	}
}

func TestAtomicWriteFileUpdatesSymlinkTarget(t *testing.T) {
	tmpDir := t.TempDir()
	target := filepath.Join(tmpDir, "target.txt")
	link := filepath.Join(tmpDir, "link.txt")

	if err := os.WriteFile(target, []byte("old"), 0o644); err != nil {
		t.Fatalf("failed to seed target file: %v", err)
	}
	if err := os.Symlink("target.txt", link); err != nil {
		var pathErr *os.PathError
		if errors.As(err, &pathErr) {
			t.Skipf("symlink unsupported in this environment: %v", err)
		}
		t.Fatalf("failed to create symlink: %v", err)
	}

	if err := atomicWriteFile(link, []byte("new"), 0o644); err != nil {
		t.Fatalf("atomicWriteFile on symlink failed: %v", err)
	}

	linkInfo, err := os.Lstat(link)
	if err != nil {
		t.Fatalf("failed to lstat symlink path: %v", err)
	}
	if linkInfo.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("expected symlink to remain symlink")
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("failed to read target after write: %v", err)
	}
	if string(got) != "new" {
		t.Fatalf("expected target content to be updated, got %q", string(got))
	}
}
