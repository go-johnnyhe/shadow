package client_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-johnnyhe/shadow/internal/client"
	"github.com/go-johnnyhe/shadow/server"
	"github.com/gorilla/websocket"
	"net/http"
	"net/http/httptest"
)

func TestSmokeSyncNearLimitFile(t *testing.T) {
	server.SetReadOnlyJoiners(false)

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", server.StartServer)
	httpServer := httptest.NewServer(mux)
	defer httpServer.Close()

	wsURL := "ws" + strings.TrimPrefix(httpServer.URL, "http") + "/ws"
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

	hostConn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("host dial failed: %v", err)
	}
	defer hostConn.Close()

	joinConn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("join dial failed: %v", err)
	}
	defer joinConn.Close()

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
	if count != 1 {
		t.Fatalf("expected to send 1 file, sent %d", count)
	}

	joinFilePath := filepath.Join(joinDir, relPath)
	deadline := time.Now().Add(6 * time.Second)
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

func TestSmokeSyncRenameAndDelete(t *testing.T) {
	server.SetReadOnlyJoiners(false)

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", server.StartServer)
	httpServer := httptest.NewServer(mux)
	defer httpServer.Close()

	wsURL := "ws" + strings.TrimPrefix(httpServer.URL, "http") + "/ws"
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

	hostConn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("host dial failed: %v", err)
	}
	defer hostConn.Close()

	joinConn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("join dial failed: %v", err)
	}
	defer joinConn.Close()

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
