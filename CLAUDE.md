# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Build & Development Commands

```bash
# Build the binary
go build -o shadow .

# Run directly without building
go run . [start|join] [args]

# Run with version injection (as goreleaser does)
go build -ldflags "-X main.version=1.0.0" -o shadow .

# Install dependencies
go mod download

# Tidy dependencies
go mod tidy
```

## Architecture Overview

Shadow is a real-time file collaboration tool. The architecture follows a client-server model with end-to-end encryption where the server acts as a dumb message relay.

```
Host Client ←──encrypted──→ WebSocket Server ←──encrypted──→ Joiner Client
     │                            │                               │
     └── E2E encrypt/decrypt      └── Relay only (no decrypt)     └── E2E encrypt/decrypt
```

### Key Components

**Entry Flow** (`main.go` → `cmd/root.go` → `cmd/interactive.go` → `cmd/session.go`):
- `main.go` sets version, calls `cmd.Execute()`
- `root.go` handles CLI setup with Cobra, detects interactive mode
- `interactive.go` provides TUI form wizard using charmbracelet/huh
- `session.go` contains `runStart()` and `runJoin()` which orchestrate sessions

**Client** (`internal/client/sync.go`):
- File watching via fsnotify with 50ms debounce
- Sends/receives encrypted file payloads
- SHA256 hash tracking to avoid duplicate sends
- `isWritingReceivedFile` flag prevents echo loops

**Server** (`server/websocket.go`):
- Pure message relay - broadcasts encrypted messages to all peers
- Handles read-only-joiners mode via control channel
- 30s heartbeat ping/pong for connection health

**Encryption** (`internal/e2e/codec.go`):
- AES-256-GCM with HMAC-SHA256 key derivation
- Key seed passed in URL fragment (never sent to server)
- Random 12-byte nonce per message

**Tunnel** (`internal/tunnel/cloudflared.go`):
- Downloads cloudflared binary to `~/.shadow/` on first run
- Creates public HTTPS tunnel to local WebSocket server

### Protocol

Messages use format: `<channel>|<payload>`
- `__shadow_control__` - Control messages (e.g., `read_only_joiners=1`)
- `__shadow_e2e__` - Encrypted file content (base64 of nonce+ciphertext)

File payload format (before encryption): `relative/path|base64-encoded-content`

### Ignored Patterns

The client ignores: `.git`, `.hg`, `.svn`, `.vscode`, `.idea`, `node_modules`, `.DS_Store`, vim swap files (`.sw[a-p]`, `.swp`, `.swo`), temp files (`.tmp`, `~` suffix).
