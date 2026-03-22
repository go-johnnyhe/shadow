package mcp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"time"
)

// Session states mirror the VS Code extension's state machine.
type SessionState string

const (
	StateIdle          SessionState = "idle"
	StateStarting      SessionState = "starting"
	StateRunningHost   SessionState = "running_host"
	StateRunningJoiner SessionState = "running_joiner"
	StateStopping      SessionState = "stopping"
	StateError         SessionState = "error"
)

// Event name constants — local copy to avoid cross-package dependency.
const (
	eventStarting         = "starting"
	eventTunnelReady      = "tunnel_ready"
	eventConnected        = "connected"
	eventSnapshotComplete = "snapshot_complete"
	eventStopped          = "stopped"
	eventFileSent         = "file_sent"
	eventFileReceived     = "file_received"
	eventReadOnly         = "read_only"
	eventError            = "error"
)

// jsonEvent mirrors cmd.JSONEvent for parsing child process stdout.
type jsonEvent struct {
	Event       string `json:"event"`
	Message     string `json:"message"`
	JoinURL     string `json:"join_url,omitempty"`
	JoinCommand string `json:"join_command,omitempty"`
	FileCount   int    `json:"file_count,omitempty"`
	RelPath     string `json:"rel_path,omitempty"`
	Timestamp   string `json:"timestamp"`
}

// SessionInfo holds runtime details about the active session.
type SessionInfo struct {
	Mode          string   `json:"mode"` // "host" or "joiner"
	JoinURL       string   `json:"join_url,omitempty"`
	JoinCommand   string   `json:"join_command,omitempty"`
	WorkspacePath string   `json:"workspace_path,omitempty"`
	FileCount     int      `json:"file_count,omitempty"`
	ReadOnly      bool     `json:"read_only,omitempty"`
	RecentFiles   []string `json:"recent_files,omitempty"`
	LastError     string   `json:"last_error,omitempty"`
}

// SessionManager manages a single shadow child process and its state.
type SessionManager struct {
	mu              sync.Mutex
	state           SessionState
	info            *SessionInfo
	proc            *exec.Cmd
	sawStoppedEvent bool

	// ready is closed when the session reaches a running state or errors.
	ready chan struct{}
	// done is closed when the child process exits.
	done chan struct{}
}

// NewSessionManager creates an idle session manager.
func NewSessionManager() *SessionManager {
	return &SessionManager{
		state: StateIdle,
	}
}

// State returns the current session state.
func (sm *SessionManager) State() SessionState {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	return sm.state
}

// Info returns a copy of the current session info, or nil if idle.
func (sm *SessionManager) Info() *SessionInfo {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if sm.info == nil {
		return nil
	}
	cp := *sm.info
	recentCopy := make([]string, len(sm.info.RecentFiles))
	copy(recentCopy, sm.info.RecentFiles)
	cp.RecentFiles = recentCopy
	return &cp
}

// Start launches a host session. Blocks until the tunnel is ready or an error occurs.
func (sm *SessionManager) Start(path string, readOnlyJoiners bool) (*SessionInfo, error) {
	sm.mu.Lock()
	if sm.state != StateIdle && sm.state != StateError {
		sm.mu.Unlock()
		return nil, fmt.Errorf("session already active (state: %s)", sm.state)
	}

	sm.info = &SessionInfo{Mode: "host", WorkspacePath: path}
	sm.ready = make(chan struct{})
	sm.done = make(chan struct{})
	sm.sawStoppedEvent = false

	args := []string{"start", path, "--json", "--force"}
	if readOnlyJoiners {
		args = append(args, "--read-only-joiners")
	}
	sm.mu.Unlock()

	if err := sm.spawn(args); err != nil {
		sm.mu.Lock()
		sm.setState(StateError)
		sm.info.LastError = err.Error()
		sm.mu.Unlock()
		return nil, err
	}

	// Block until running or error.
	<-sm.ready

	sm.mu.Lock()
	defer sm.mu.Unlock()
	if sm.state == StateError {
		return nil, fmt.Errorf("session failed: %s", sm.info.LastError)
	}
	cp := *sm.info
	return &cp, nil
}

// Join connects to an existing session. Blocks until connected or error.
func (sm *SessionManager) Join(url, path string) (*SessionInfo, error) {
	sm.mu.Lock()
	if sm.state != StateIdle && sm.state != StateError {
		sm.mu.Unlock()
		return nil, fmt.Errorf("session already active (state: %s)", sm.state)
	}

	if path == "" {
		path = "."
	}
	sm.info = &SessionInfo{Mode: "joiner", JoinURL: url, WorkspacePath: path}
	sm.ready = make(chan struct{})
	sm.done = make(chan struct{})
	sm.sawStoppedEvent = false

	args := []string{"join", url, "--json", "--path", path}
	sm.mu.Unlock()

	if err := sm.spawn(args); err != nil {
		sm.mu.Lock()
		sm.setState(StateError)
		sm.info.LastError = err.Error()
		sm.mu.Unlock()
		return nil, err
	}

	<-sm.ready

	sm.mu.Lock()
	defer sm.mu.Unlock()
	if sm.state == StateError {
		return nil, fmt.Errorf("session failed: %s", sm.info.LastError)
	}
	cp := *sm.info
	return &cp, nil
}

// Stop gracefully stops the active session.
func (sm *SessionManager) Stop() error {
	sm.mu.Lock()
	if sm.proc == nil || sm.state == StateIdle || sm.state == StateStopping {
		st := sm.state
		sm.mu.Unlock()
		if st == StateIdle {
			return fmt.Errorf("no active session")
		}
		return nil // already stopping
	}
	proc := sm.proc
	done := sm.done
	sm.setState(StateStopping)
	sm.mu.Unlock()

	// Send SIGINT for graceful shutdown.
	if err := proc.Process.Signal(os.Interrupt); err != nil {
		// Process may already be dead.
		proc.Process.Kill()
	}

	// Wait up to 5s, then force kill.
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		proc.Process.Kill()
		<-done
	}

	return nil
}

// spawn starts the child process and begins reading its stdout.
func (sm *SessionManager) spawn(args []string) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to find executable: %w", err)
	}

	sm.mu.Lock()
	sm.setState(StateStarting)

	cmd := exec.Command(exe, args...)
	cmd.Env = append(os.Environ(), "NO_COLOR=1")
	cmd.Stdin = nil

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		sm.mu.Unlock()
		return fmt.Errorf("failed to create stdout pipe: %w", err)
	}
	// Discard stderr — we don't need it.
	cmd.Stderr = nil

	if err := cmd.Start(); err != nil {
		sm.mu.Unlock()
		return fmt.Errorf("failed to start process: %w", err)
	}
	sm.proc = cmd
	sm.mu.Unlock()

	// Read stdout lines in a goroutine.
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			sm.parseLine(scanner.Text())
		}
	}()

	// Monitor process exit in a goroutine.
	go func() {
		cmd.Wait()

		sm.mu.Lock()
		defer sm.mu.Unlock()
		sm.proc = nil

		if sm.state == StateStopping || sm.sawStoppedEvent {
			sm.setState(StateIdle)
			sm.info = nil
		} else if sm.state != StateIdle && sm.state != StateError {
			if sm.info != nil {
				sm.info.LastError = "process exited unexpectedly"
			}
			sm.setState(StateError)
		}

		// Signal anyone waiting.
		sm.closeReady()
		close(sm.done)
	}()

	return nil
}

// parseLine processes a single JSON line from the child process stdout.
func (sm *SessionManager) parseLine(line string) {
	if line == "" {
		return
	}

	var evt jsonEvent
	if err := json.Unmarshal([]byte(line), &evt); err != nil {
		return // non-JSON line, ignore
	}

	sm.mu.Lock()
	defer sm.mu.Unlock()

	switch evt.Event {
	case eventStarting:
		// Already in Starting state.

	case eventTunnelReady:
		if sm.info != nil {
			sm.info.JoinURL = evt.JoinURL
			sm.info.JoinCommand = evt.JoinCommand
		}
		sm.setState(StateRunningHost)
		sm.closeReady()

	case eventConnected:
		sm.setState(StateRunningJoiner)
		sm.closeReady()

	case eventSnapshotComplete:
		if sm.info != nil {
			sm.info.FileCount = evt.FileCount
		}

	case eventFileSent, eventFileReceived:
		if sm.info != nil {
			sm.addRecentFile(evt.RelPath)
		}

	case eventReadOnly:
		if sm.info != nil {
			sm.info.ReadOnly = true
		}

	case eventError:
		if sm.info != nil {
			sm.info.LastError = evt.Message
		}
		sm.setState(StateError)
		sm.closeReady()

	case eventStopped:
		sm.sawStoppedEvent = true
		if sm.state != StateStopping {
			sm.setState(StateStopping)
		}
	}
}

// addRecentFile appends a file path to the recent files list (max 20).
func (sm *SessionManager) addRecentFile(relPath string) {
	if relPath == "" {
		return
	}
	sm.info.RecentFiles = append(sm.info.RecentFiles, relPath)
	if len(sm.info.RecentFiles) > 20 {
		sm.info.RecentFiles = sm.info.RecentFiles[len(sm.info.RecentFiles)-20:]
	}
}

// setState updates state (caller must hold mu).
func (sm *SessionManager) setState(s SessionState) {
	sm.state = s
}

// closeReady closes the ready channel if it hasn't been closed yet.
func (sm *SessionManager) closeReady() {
	select {
	case <-sm.ready:
		// already closed
	default:
		close(sm.ready)
	}
}
