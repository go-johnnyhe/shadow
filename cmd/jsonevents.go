package cmd

import (
	"encoding/json"
	"os"
	"time"

	"github.com/go-johnnyhe/shadow/internal/tunnel"
)

// Event name constants — single source of truth for the JSON protocol.
const (
	EventStarting         = "starting"
	EventTunnelReady      = "tunnel_ready"
	EventConnected        = "connected"
	EventSnapshotComplete = "snapshot_complete"
	EventStopped          = "stopped"
	EventFileSent         = "file_sent"
	EventFileReceived     = "file_received"
	EventReadOnly         = "read_only"
	EventWarning          = "warning"
	EventError            = "error"
	EventDownloadingDep   = "downloading_dependency"
	EventDependencyReady  = "dependency_ready"
)

// JSONEvent represents a structured event emitted in --json mode.
type JSONEvent struct {
	Event       string `json:"event"`
	Message     string `json:"message"`
	JoinURL     string `json:"join_url,omitempty"`
	JoinCommand string `json:"join_command,omitempty"`
	FileCount   int    `json:"file_count,omitempty"`
	RelPath     string `json:"rel_path,omitempty"`
	Timestamp   string `json:"timestamp"`
}

func emitJSON(evt JSONEvent) {
	evt.Timestamp = time.Now().UTC().Format(time.RFC3339)
	data, err := json.Marshal(evt)
	if err != nil {
		return
	}
	data = append(data, '\n')
	os.Stdout.Write(data)
}

func emitJSONError(msg string) {
	emitJSON(JSONEvent{Event: EventError, Message: msg})
}

// jsonOnEvent returns an OnEvent callback that emits JSON events,
// or nil if jsonMode is false.
func jsonOnEvent(jsonMode bool) func(string, string, string) {
	if !jsonMode {
		return nil
	}
	return func(eventType, relPath, message string) {
		emitJSON(JSONEvent{Event: eventType, RelPath: relPath, Message: message})
	}
}

func tunnelStatusReporter(jsonMode bool) tunnel.StatusReporter {
	if !jsonMode {
		return nil
	}
	return func(event, message string) {
		emitJSON(JSONEvent{Event: event, Message: message})
	}
}
