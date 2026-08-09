package client_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-johnnyhe/shadow/internal/client"
	"github.com/go-johnnyhe/shadow/internal/protocol"
	"github.com/go-johnnyhe/shadow/server"
	"github.com/gorilla/websocket"
	"net/http"
	"net/http/httptest"
)

const (
	smokeHostToken = "smoke-host-token"
	smokeJoinToken = "smoke-join-token"
)

func newSmokeServer(t *testing.T, readOnly bool) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle("/ws", server.NewRelay(server.SessionConfig{
		ReadOnlyJoiners: readOnly,
		HostToken:       smokeHostToken,
		JoinToken:       smokeJoinToken,
	}))
	httpServer := httptest.NewServer(mux)
	t.Cleanup(httpServer.Close)
	return "ws" + strings.TrimPrefix(httpServer.URL, "http") + "/ws"
}

func dialSmoke(t *testing.T, wsURL, token string) *websocket.Conn {
	t.Helper()
	dialer := *websocket.DefaultDialer
	dialer.Subprotocols = []string{protocol.WebSocketSubprotocol}
	header := http.Header{}
	header.Set("Authorization", "Bearer "+token)
	conn, _, err := dialer.Dial(wsURL, header)
	if err != nil {
		t.Fatalf("websocket dial failed: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func TestSmokeSyncNearLimitFile(t *testing.T) {
	wsURL := newSmokeServer(t, false)
	hostDir := t.TempDir()
	joinDir := t.TempDir()
	relPath := filepath.Join("nested", "big.txt")
	hostFilePath := filepath.Join(hostDir, relPath)
	if err := os.MkdirAll(filepath.Dir(hostFilePath), 0o755); err != nil {
		t.Fatalf("failed to create host nested dir: %v", err)
	}

	// 8MB raw file; base64+encryption expands above the old 10MB wire check.
	payload := bytes.Repeat([]byte("shadow-sync-"), (8*1024*1024)/len("shadow-sync-"))
	if err := os.WriteFile(hostFilePath, payload, 0o644); err != nil {
		t.Fatalf("failed to create host file: %v", err)
	}

	hostConn := dialSmoke(t, wsURL, smokeHostToken)
	joinConn := dialSmoke(t, wsURL, smokeJoinToken)

	key := "smoke-test-key"
	hostClient, err := client.NewClient(hostConn, client.Options{
		IsHost:  true,
		E2EKey:  key,
		BaseDir: hostDir,
	})
	if err != nil {
		t.Fatalf("failed to create host client: %v", err)
	}
	joinClient, err := client.NewClient(joinConn, client.Options{
		E2EKey:  key,
		BaseDir: joinDir,
	})
	if err != nil {
		t.Fatalf("failed to create join client: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hostClient.Start(ctx)
	joinClient.Start(ctx)

	count, err := hostClient.SendInitialSnapshot()
	if err != nil {
		t.Fatalf("initial snapshot failed: %v", err)
	}
	if count < 0 || count > 1 {
		t.Fatalf("unexpected initial snapshot count: %d", count)
	}

	joinFilePath := filepath.Join(joinDir, relPath)
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		got, readErr := os.ReadFile(joinFilePath)
		if readErr == nil {
			if !bytes.Equal(got, payload) {
				t.Fatalf("synced bytes mismatch: got=%d want=%d", len(got), len(payload))
			}
			return
		}
		time.Sleep(30 * time.Millisecond)
	}

	t.Fatalf("timed out waiting for %s to sync", joinFilePath)
}

func TestHostSyncsExistingFilesWhenJoinerConnectsLater(t *testing.T) {
	wsURL := newSmokeServer(t, false)
	hostDir := t.TempDir()
	joinDir := t.TempDir()
	hostFilePath := filepath.Join(hostDir, "existing.txt")
	want := []byte("created before the joiner connected")
	if err := os.WriteFile(hostFilePath, want, 0o644); err != nil {
		t.Fatalf("failed to create host file: %v", err)
	}

	hostConn := dialSmoke(t, wsURL, smokeHostToken)

	key := "late-join-test-key"
	hostClient, err := client.NewClient(hostConn, client.Options{
		IsHost:  true,
		E2EKey:  key,
		BaseDir: hostDir,
	})
	if err != nil {
		t.Fatalf("failed to create host client: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hostClient.Start(ctx)
	if count, snapshotErr := hostClient.SendInitialSnapshot(); snapshotErr != nil || count != 1 {
		t.Fatalf("initial snapshot before join = (%d, %v), want (1, nil)", count, snapshotErr)
	}

	joinConn := dialSmoke(t, wsURL, smokeJoinToken)
	joinClient, err := client.NewClient(joinConn, client.Options{
		E2EKey:  key,
		BaseDir: joinDir,
	})
	if err != nil {
		t.Fatalf("failed to create join client: %v", err)
	}
	joinClient.Start(ctx)

	waitForFileContent(t, filepath.Join(joinDir, "existing.txt"), want, 6*time.Second)
}

func TestClientRejectsPlaintextFileMessage(t *testing.T) {
	wsURL := newSmokeServer(t, false)
	attackerConn := dialSmoke(t, wsURL, smokeHostToken)

	joinDir := t.TempDir()
	joinConn := dialSmoke(t, wsURL, smokeJoinToken)
	joinClient, err := client.NewClient(joinConn, client.Options{
		E2EKey:  "key-the-attacker-does-not-have",
		BaseDir: joinDir,
	})
	if err != nil {
		t.Fatalf("failed to create join client: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	joinClient.Start(ctx)

	plaintext := "injected.txt|" + base64.StdEncoding.EncodeToString([]byte("not encrypted"))
	if err := attackerConn.WriteMessage(websocket.TextMessage, []byte(plaintext)); err != nil {
		t.Fatalf("failed to send plaintext attack message: %v", err)
	}
	time.Sleep(250 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(joinDir, "injected.txt")); !os.IsNotExist(err) {
		t.Fatalf("plaintext message created a file: %v", err)
	}
}

func TestSmokeSyncRenameAndDelete(t *testing.T) {
	wsURL := newSmokeServer(t, false)
	hostDir := t.TempDir()
	joinDir := t.TempDir()
	oldRelPath := filepath.Join("nested", "old.txt")
	newRelPath := filepath.Join("nested", "new.txt")
	oldHostPath := filepath.Join(hostDir, oldRelPath)
	newHostPath := filepath.Join(hostDir, newRelPath)
	if err := os.MkdirAll(filepath.Dir(oldHostPath), 0o755); err != nil {
		t.Fatalf("failed to create host nested dir: %v", err)
	}
	initial := []byte("hello from host")
	if err := os.WriteFile(oldHostPath, initial, 0o644); err != nil {
		t.Fatalf("failed to create host file: %v", err)
	}

	hostConn := dialSmoke(t, wsURL, smokeHostToken)
	joinConn := dialSmoke(t, wsURL, smokeJoinToken)

	key := "smoke-rename-delete-key"
	hostClient, err := client.NewClient(hostConn, client.Options{
		IsHost:  true,
		E2EKey:  key,
		BaseDir: hostDir,
	})
	if err != nil {
		t.Fatalf("failed to create host client: %v", err)
	}
	joinClient, err := client.NewClient(joinConn, client.Options{
		E2EKey:  key,
		BaseDir: joinDir,
	})
	if err != nil {
		t.Fatalf("failed to create join client: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hostClient.Start(ctx)
	joinClient.Start(ctx)

	if _, err := hostClient.SendInitialSnapshot(); err != nil {
		t.Fatalf("initial snapshot failed: %v", err)
	}

	oldJoinPath := filepath.Join(joinDir, oldRelPath)
	waitForFileContent(t, oldJoinPath, initial, 6*time.Second)

	if err := os.Rename(oldHostPath, newHostPath); err != nil {
		t.Fatalf("failed to rename host file: %v", err)
	}

	newJoinPath := filepath.Join(joinDir, newRelPath)
	waitForFileContent(t, newJoinPath, initial, 6*time.Second)
	waitForPathRemoved(t, oldJoinPath, 6*time.Second)

	if err := os.Remove(newHostPath); err != nil {
		t.Fatalf("failed to delete host file: %v", err)
	}
	waitForPathRemoved(t, newJoinPath, 6*time.Second)
}

func TestConcurrentEditsConvergeAndPreserveBothVersions(t *testing.T) {
	wsURL := newSmokeServer(t, false)
	hostDir := t.TempDir()
	joinDir := t.TempDir()
	hostPath := filepath.Join(hostDir, "shared.txt")
	joinPath := filepath.Join(joinDir, "shared.txt")
	base := []byte("common base")
	if err := os.WriteFile(hostPath, base, 0o644); err != nil {
		t.Fatal(err)
	}

	hostClient, err := client.NewClient(dialSmoke(t, wsURL, smokeHostToken), client.Options{IsHost: true, E2EKey: "concurrent-key", BaseDir: hostDir})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hostClient.Start(ctx)
	if _, err := hostClient.SendInitialSnapshot(); err != nil {
		t.Fatal(err)
	}

	joinClient, err := client.NewClient(dialSmoke(t, wsURL, smokeJoinToken), client.Options{E2EKey: "concurrent-key", BaseDir: joinDir})
	if err != nil {
		t.Fatal(err)
	}
	joinClient.Start(ctx)
	readyCtx, readyCancel := context.WithTimeout(ctx, 6*time.Second)
	defer readyCancel()
	if err := joinClient.WaitReady(readyCtx); err != nil {
		t.Fatalf("joiner did not become ready: %v", err)
	}
	waitForFileContent(t, joinPath, base, 6*time.Second)

	hostEdit := []byte("host concurrent edit")
	joinEdit := []byte("join concurrent edit")
	if err := os.WriteFile(hostPath, hostEdit, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(joinPath, joinEdit, 0o644); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	done := make(chan struct{}, 2)
	go func() { <-start; hostClient.SendFile(hostPath); done <- struct{}{} }()
	go func() { <-start; joinClient.SendFile(joinPath); done <- struct{}{} }()
	close(start)
	<-done
	<-done

	deadline := time.Now().Add(6 * time.Second)
	var final []byte
	for time.Now().Before(deadline) {
		hostFinal, hostErr := os.ReadFile(hostPath)
		joinFinal, joinErr := os.ReadFile(joinPath)
		if hostErr == nil && joinErr == nil && bytes.Equal(hostFinal, joinFinal) &&
			(bytes.Equal(hostFinal, hostEdit) || bytes.Equal(hostFinal, joinEdit)) {
			final = hostFinal
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if final == nil {
		t.Fatal("clients did not converge after concurrent edits")
	}
	if !bytes.Equal(final, hostEdit) && !conflictContentExists(hostDir, hostEdit) && !conflictContentExists(joinDir, hostEdit) {
		t.Fatal("host edit was lost")
	}
	if !bytes.Equal(final, joinEdit) && !conflictContentExists(hostDir, joinEdit) && !conflictContentExists(joinDir, joinEdit) {
		t.Fatal("joiner edit was lost")
	}
}

func conflictContentExists(baseDir string, expected []byte) bool {
	found := false
	_ = filepath.WalkDir(filepath.Join(baseDir, ".shadow-conflicts"), func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return nil
		}
		content, readErr := os.ReadFile(path)
		if readErr == nil && bytes.Equal(content, expected) {
			found = true
		}
		return nil
	})
	return found
}

func waitForFileContent(t *testing.T, path string, want []byte, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		got, readErr := os.ReadFile(path)
		if readErr == nil {
			if !bytes.Equal(got, want) {
				t.Fatalf("synced bytes mismatch for %s: got=%d want=%d", path, len(got), len(want))
			}
			return
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s to sync", path)
}

func waitForPathRemoved(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		_, err := os.Stat(path)
		if os.IsNotExist(err) {
			return
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s to be removed", path)
}
