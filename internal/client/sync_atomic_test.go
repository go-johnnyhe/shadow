package client

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-johnnyhe/shadow/internal/e2e"
	"github.com/go-johnnyhe/shadow/internal/protocol"
)

func TestAtomicWriteFilePreservesExistingMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not preserve Unix permission bits")
	}
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

func TestAtomicWriteFileReplacesSymlinkWithoutUpdatingTarget(t *testing.T) {
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
		t.Fatalf("failed to lstat replaced path: %v", err)
	}
	if linkInfo.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("expected incoming file to replace symlink")
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("failed to read target after write: %v", err)
	}
	if string(got) != "old" {
		t.Fatalf("expected symlink target to remain unchanged, got %q", string(got))
	}
	got, err = os.ReadFile(link)
	if err != nil {
		t.Fatalf("failed to read replacement file: %v", err)
	}
	if string(got) != "new" {
		t.Fatalf("expected replacement content, got %q", string(got))
	}
}

func TestSecureIncomingDestinationRejectsSymlinkedParent(t *testing.T) {
	baseDir := t.TempDir()
	outsideDir := t.TempDir()
	linkedDir := filepath.Join(baseDir, "linked")
	if err := os.Symlink(outsideDir, linkedDir); err != nil {
		var pathErr *os.PathError
		if errors.As(err, &pathErr) {
			t.Skipf("symlink unsupported in this environment: %v", err)
		}
		t.Fatalf("failed to create directory symlink: %v", err)
	}

	if _, err := secureIncomingDestination(baseDir, "linked/outside.txt"); err == nil {
		t.Fatalf("expected symlinked parent directory to be rejected")
	}
}

func TestSingleFileClientRejectsSiblingOperations(t *testing.T) {
	baseDir := t.TempDir()
	sibling := filepath.Join(baseDir, "sibling.txt")
	if err := os.WriteFile(sibling, []byte("safe"), 0o644); err != nil {
		t.Fatal(err)
	}
	client := testApplyClient(t, baseDir)
	client.singleFileRel = "shared.txt"

	for _, operation := range []protocol.SyncOperation{
		{ID: "attacker-1", Path: "sibling.txt", BaseState: fileHash([]byte("safe")), DesiredHash: fileHash([]byte("changed")), Content: []byte("changed")},
		{ID: "attacker-2", Path: "sibling.txt", BaseState: fileHash([]byte("safe")), DesiredHash: missingState, Delete: true},
	} {
		encrypted := encryptedOperation(t, client.codec, operation)
		if err := client.applyEncryptedOperation(encrypted, false); err == nil {
			t.Fatalf("operation outside single-file scope was accepted: %+v", operation)
		}
		got, err := os.ReadFile(sibling)
		if err != nil || string(got) != "safe" {
			t.Fatalf("sibling changed after rejected operation: %q, %v", got, err)
		}
	}
}

func TestSingleFileBootstrapRejectsSymlinkedParent(t *testing.T) {
	baseDir := t.TempDir()
	outsideDir := t.TempDir()
	outsideFile := filepath.Join(outsideDir, "local.txt")
	if err := os.WriteFile(outsideFile, []byte("outside work"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideDir, filepath.Join(baseDir, "linked")); err != nil {
		var pathErr *os.PathError
		if errors.As(err, &pathErr) {
			t.Skipf("symlink unsupported in this environment: %v", err)
		}
		t.Fatal(err)
	}

	client := testApplyClient(t, baseDir)
	err := client.applyBootstrapManifest(protocol.BootstrapManifest{
		Version:    protocol.SyncProtocolVersion,
		Type:       protocol.BootstrapManifestType,
		SingleFile: "linked/local.txt",
	})
	if err == nil {
		t.Fatal("bootstrap accepted a single-file path below a symlink")
	}
	got, readErr := os.ReadFile(outsideFile)
	if readErr != nil || string(got) != "outside work" {
		t.Fatalf("outside file changed: %q, %v", got, readErr)
	}
}

func TestIncomingConflictPreservesLocalBytes(t *testing.T) {
	baseDir := t.TempDir()
	destination := filepath.Join(baseDir, "file.txt")
	local := []byte("local edit")
	incoming := []byte("ordered edit")
	if err := os.WriteFile(destination, local, 0o755); err != nil {
		t.Fatal(err)
	}
	client := testApplyClient(t, baseDir)
	client.lastHash.Store("file.txt", fileHash([]byte("common base")))
	operation := protocol.SyncOperation{
		ID:          "remote-1",
		Path:        "file.txt",
		BaseState:   fileHash([]byte("common base")),
		DesiredHash: fileHash(incoming),
		Content:     incoming,
	}
	if err := client.applyEncryptedOperation(encryptedOperation(t, client.codec, operation), false); err != nil {
		t.Fatalf("apply failed: %v", err)
	}
	got, err := os.ReadFile(destination)
	if err != nil || string(got) != string(incoming) {
		t.Fatalf("live file = %q, %v", got, err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(destination)
		if err != nil {
			t.Fatalf("stat incoming file: %v", err)
		}
		if info.Mode().Perm() != 0o755 {
			t.Fatalf("file mode was not preserved: %v", info.Mode().Perm())
		}
	}
	if !conflictTreeContains(t, baseDir, local) {
		t.Fatal("local edit was not preserved in conflict directory")
	}
}

func TestIncomingDirectoryDeletePreservesUnsentChildEdit(t *testing.T) {
	baseDir := t.TempDir()
	local := []byte("unsent child edit")
	directory := filepath.Join(baseDir, "docs")
	if err := os.Mkdir(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "draft.txt"), local, 0o644); err != nil {
		t.Fatal(err)
	}

	client := testApplyClient(t, baseDir)
	client.lastHash.Store("docs", directoryState)
	operation := protocol.SyncOperation{
		ID:          "remote-delete",
		Path:        "docs",
		BaseState:   directoryState,
		DesiredHash: missingState,
		Delete:      true,
	}
	if err := client.applyEncryptedOperation(encryptedOperation(t, client.codec, operation), false); err != nil {
		t.Fatalf("apply failed: %v", err)
	}
	if _, err := os.Lstat(directory); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("deleted directory remains live: %v", err)
	}
	if !conflictTreeContains(t, baseDir, local) {
		t.Fatal("unsent child edit was not preserved")
	}
}

func TestIncomingFilePreservesObstructingParent(t *testing.T) {
	baseDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(baseDir, "parent"), []byte("local parent file"), 0o644); err != nil {
		t.Fatal(err)
	}
	client := testApplyClient(t, baseDir)
	incoming := []byte("nested content")
	operation := protocol.SyncOperation{
		ID:          "remote-2",
		Path:        "parent/file.txt",
		BaseState:   missingState,
		DesiredHash: fileHash(incoming),
		Content:     incoming,
	}
	if err := client.applyEncryptedOperation(encryptedOperation(t, client.codec, operation), false); err != nil {
		t.Fatalf("apply failed: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(baseDir, "parent", "file.txt"))
	if err != nil || string(got) != string(incoming) {
		t.Fatalf("nested file = %q, %v", got, err)
	}
	if !conflictTreeContains(t, baseDir, []byte("local parent file")) {
		t.Fatal("obstructing parent was not preserved")
	}
}

func TestBootstrapManifestPreservesPathsMissingFromHost(t *testing.T) {
	baseDir := t.TempDir()
	extra := []byte("joiner-only work")
	if err := os.WriteFile(filepath.Join(baseDir, "extra.txt"), extra, 0o644); err != nil {
		t.Fatal(err)
	}
	client := testApplyClient(t, baseDir)
	if err := client.applyBootstrapManifest(protocol.BootstrapManifest{Version: protocol.SyncProtocolVersion, Type: protocol.BootstrapManifestType}); err != nil {
		t.Fatalf("manifest apply failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(baseDir, "extra.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("joiner-only path remained live: %v", err)
	}
	if !conflictTreeContains(t, baseDir, extra) {
		t.Fatal("joiner-only path was not preserved")
	}
}

func TestRenameRescanIsDebouncedAcrossPaths(t *testing.T) {
	var rescans atomic.Int32
	client := &Client{rescan: func() { rescans.Add(1) }}
	for i := 0; i < 100; i++ {
		client.scheduleRenameRescan()
	}
	time.Sleep(2 * renameRescanDelay)
	if got := rescans.Load(); got != 1 {
		t.Fatalf("rescans = %d, want 1", got)
	}
	client.stopping.Store(true)
	client.scheduleRenameRescan()
	time.Sleep(2 * renameRescanDelay)
	if got := rescans.Load(); got != 1 {
		t.Fatalf("rescan ran after stop: %d", got)
	}
}

func TestDisconnectedEventIsConcise(t *testing.T) {
	var eventType, message string
	client := &Client{onEvent: func(gotType, _ string, gotMessage string) {
		eventType = gotType
		message = gotMessage
	}}
	client.notifyDisconnected()
	if eventType != "disconnected" || message != "Disconnected" {
		t.Fatalf("disconnect event = (%q, %q)", eventType, message)
	}
}

func testApplyClient(t *testing.T, baseDir string) *Client {
	t.Helper()
	codec, err := e2e.NewCodec("test-key")
	if err != nil {
		t.Fatal(err)
	}
	ignore := NewOutboundIgnore(baseDir)
	t.Cleanup(ignore.Close)
	return &Client{
		codec:          codec,
		baseDir:        baseDir,
		outboundIgnore: ignore,
		fileTimers:     make(map[string]*time.Timer),
		pending:        make(map[string][]pendingOperation),
	}
}

func encryptedOperation(t *testing.T, codec *e2e.Codec, operation protocol.SyncOperation) string {
	t.Helper()
	plaintext, err := protocol.EncodeSyncOperation(operation)
	if err != nil {
		t.Fatal(err)
	}
	encrypted, err := codec.Encrypt(plaintext)
	if err != nil {
		t.Fatal(err)
	}
	return encrypted
}

func conflictTreeContains(t *testing.T, baseDir string, content []byte) bool {
	t.Helper()
	found := false
	_ = filepath.WalkDir(filepath.Join(baseDir, conflictDirectory), func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return nil
		}
		got, readErr := os.ReadFile(path)
		if readErr == nil && string(got) == string(content) {
			found = true
		}
		return nil
	})
	return found
}
