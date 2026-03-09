package cmd

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestEmitJSON_Shape(t *testing.T) {
	// Capture stdout.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	origStdout := os.Stdout
	os.Stdout = w

	emitJSON(JSONEvent{
		Event:       EventTunnelReady,
		Message:     "Tunnel ready",
		JoinURL:     "https://example.trycloudflare.com#key123",
		JoinCommand: "shadow join 'https://example.trycloudflare.com#key123'",
	})

	w.Close()
	os.Stdout = origStdout

	buf := make([]byte, 4096)
	n, _ := r.Read(buf)
	output := strings.TrimSpace(string(buf[:n]))

	var evt JSONEvent
	if err := json.Unmarshal([]byte(output), &evt); err != nil {
		t.Fatalf("output is not valid JSON: %v\nraw: %s", err, output)
	}

	if evt.Event != EventTunnelReady {
		t.Errorf("event = %q, want %q", evt.Event, EventTunnelReady)
	}
	if evt.Message != "Tunnel ready" {
		t.Errorf("message = %q, want %q", evt.Message, "Tunnel ready")
	}
	if evt.JoinURL != "https://example.trycloudflare.com#key123" {
		t.Errorf("join_url = %q, want non-empty", evt.JoinURL)
	}
	if evt.JoinCommand == "" {
		t.Error("join_command should not be empty")
	}
	if evt.Timestamp == "" {
		t.Error("timestamp should not be empty")
	}
}

func TestEmitJSON_OmitsEmpty(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	origStdout := os.Stdout
	os.Stdout = w

	emitJSON(JSONEvent{
		Event:   EventStarting,
		Message: "Starting session",
	})

	w.Close()
	os.Stdout = origStdout

	buf := make([]byte, 4096)
	n, _ := r.Read(buf)
	output := strings.TrimSpace(string(buf[:n]))

	// Verify omitempty fields are absent.
	var raw map[string]interface{}
	if err := json.Unmarshal([]byte(output), &raw); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}
	for _, key := range []string{"join_url", "join_command", "file_count", "rel_path"} {
		if _, ok := raw[key]; ok {
			t.Errorf("key %q should be omitted when empty", key)
		}
	}
}

func TestEmitJSON_SnapshotComplete(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	origStdout := os.Stdout
	os.Stdout = w

	emitJSON(JSONEvent{
		Event:     EventSnapshotComplete,
		Message:   "5 files",
		FileCount: 5,
	})

	w.Close()
	os.Stdout = origStdout

	buf := make([]byte, 4096)
	n, _ := r.Read(buf)
	output := strings.TrimSpace(string(buf[:n]))

	var evt JSONEvent
	if err := json.Unmarshal([]byte(output), &evt); err != nil {
		t.Fatal(err)
	}
	if evt.FileCount != 5 {
		t.Errorf("file_count = %d, want 5", evt.FileCount)
	}
}

func TestEmitJSONError(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	origStdout := os.Stdout
	os.Stdout = w

	emitJSONError("something broke")

	w.Close()
	os.Stdout = origStdout

	buf := make([]byte, 4096)
	n, _ := r.Read(buf)
	output := strings.TrimSpace(string(buf[:n]))

	var evt JSONEvent
	if err := json.Unmarshal([]byte(output), &evt); err != nil {
		t.Fatal(err)
	}
	if evt.Event != EventError {
		t.Errorf("event = %q, want %q", evt.Event, EventError)
	}
	if evt.Message != "something broke" {
		t.Errorf("message = %q, want %q", evt.Message, "something broke")
	}
}

func TestJsonOnEvent(t *testing.T) {
	fn := jsonOnEvent(false)
	if fn != nil {
		t.Error("jsonOnEvent(false) should return nil")
	}

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	origStdout := os.Stdout
	os.Stdout = w

	fn = jsonOnEvent(true)
	if fn == nil {
		t.Fatal("jsonOnEvent(true) should return non-nil")
	}
	fn("file_sent", "main.go", "main.go")

	w.Close()
	os.Stdout = origStdout

	buf := make([]byte, 4096)
	n, _ := r.Read(buf)
	output := strings.TrimSpace(string(buf[:n]))

	var evt JSONEvent
	if err := json.Unmarshal([]byte(output), &evt); err != nil {
		t.Fatal(err)
	}
	if evt.Event != EventFileSent {
		t.Errorf("event = %q, want %q", evt.Event, EventFileSent)
	}
	if evt.RelPath != "main.go" {
		t.Errorf("rel_path = %q, want %q", evt.RelPath, "main.go")
	}
}

func TestTunnelStatusReporter(t *testing.T) {
	reporter := tunnelStatusReporter(false)
	if reporter != nil {
		t.Fatal("tunnelStatusReporter(false) should return nil")
	}

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	origStdout := os.Stdout
	os.Stdout = w

	reporter = tunnelStatusReporter(true)
	if reporter == nil {
		t.Fatal("tunnelStatusReporter(true) should return non-nil")
	}
	reporter(EventDownloadingDep, "Downloading cloudflared (~15MB)...")

	w.Close()
	os.Stdout = origStdout

	buf := make([]byte, 4096)
	n, _ := r.Read(buf)
	output := strings.TrimSpace(string(buf[:n]))

	var evt JSONEvent
	if err := json.Unmarshal([]byte(output), &evt); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	if evt.Event != EventDownloadingDep {
		t.Fatalf("event = %q, want %q", evt.Event, EventDownloadingDep)
	}
	if evt.Message == "" {
		t.Fatal("message should not be empty")
	}
}

func TestStartOptions_JSONModeExists(t *testing.T) {
	opts := StartOptions{JSONMode: true}
	if !opts.JSONMode {
		t.Error("JSONMode should be true")
	}
}

func TestJoinOptions_JSONModeExists(t *testing.T) {
	opts := JoinOptions{JSONMode: true}
	if !opts.JSONMode {
		t.Error("JSONMode should be true")
	}
}

func TestDefaultOutput_Unchanged(t *testing.T) {
	// Verify --json defaults to false (backward compat).
	opts := StartOptions{}
	if opts.JSONMode {
		t.Error("JSONMode should default to false")
	}
}
