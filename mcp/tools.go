package mcp

import (
	"context"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
)

// RegisterTools adds all Shadow tools to the MCP server.
func RegisterTools(s MCPServer, sm *SessionManager) {
	s.AddTool(shadowStartTool(), handleStart(sm))
	s.AddTool(shadowJoinTool(), handleJoin(sm))
	s.AddTool(shadowStatusTool(), handleStatus(sm))
	s.AddTool(shadowStopTool(), handleStop(sm))
}

// --- Tool definitions ---

func shadowStartTool() mcp.Tool {
	return mcp.NewTool("shadow_start",
		mcp.WithDescription("Start hosting a Shadow file collaboration session. Creates a tunnel and returns a join URL that can be shared with a remote peer. Blocks until the tunnel is ready (~10-45s)."),
		mcp.WithString("path",
			mcp.Required(),
			mcp.Description("Path to the file or directory to share"),
		),
		mcp.WithBoolean("read_only_joiners",
			mcp.Description("If true, joiners can only view files without uploading edits"),
		),
	)
}

func shadowJoinTool() mcp.Tool {
	return mcp.NewTool("shadow_join",
		mcp.WithDescription("Join an existing Shadow file collaboration session using a session URL. Blocks until connected."),
		mcp.WithString("url",
			mcp.Required(),
			mcp.Description("The session URL (including the #key fragment) from the host"),
		),
		mcp.WithString("path",
			mcp.Description("Directory to sync files into (defaults to current directory)"),
		),
	)
}

func shadowStatusTool() mcp.Tool {
	return mcp.NewTool("shadow_status",
		mcp.WithDescription("Get the current Shadow session state and details (join URL, file count, recent file activity, errors)."),
		mcp.WithReadOnlyHintAnnotation(true),
	)
}

func shadowStopTool() mcp.Tool {
	return mcp.NewTool("shadow_stop",
		mcp.WithDescription("Stop the active Shadow session. Gracefully shuts down the connection and tunnel."),
		mcp.WithDestructiveHintAnnotation(true),
	)
}

// --- Tool handlers ---

func handleStart(sm *SessionManager) func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		path, err := req.RequireString("path")
		if err != nil {
			return mcp.NewToolResultError("missing required parameter: path"), nil
		}
		readOnly := req.GetBool("read_only_joiners", false)

		info, err := sm.Start(path, readOnly)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		return mcp.NewToolResultText(fmt.Sprintf(
			"Session started. Share this URL with your peer:\n\n%s\n\nOr tell them to run:\n%s\n\nFiles shared: %d",
			info.JoinURL, info.JoinCommand, info.FileCount,
		)), nil
	}
}

func handleJoin(sm *SessionManager) func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		url, err := req.RequireString("url")
		if err != nil {
			return mcp.NewToolResultError("missing required parameter: url"), nil
		}
		path := req.GetString("path", ".")

		info, err := sm.Join(url, path)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		msg := fmt.Sprintf("Connected to session. Syncing files to: %s", info.WorkspacePath)
		if info.ReadOnly {
			msg += "\n(Read-only mode: your edits will not be sent to the host)"
		}
		return mcp.NewToolResultText(msg), nil
	}
}

func handleStatus(sm *SessionManager) func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		state := sm.State()
		info := sm.Info()

		if state == StateIdle {
			return mcp.NewToolResultText("No active session."), nil
		}

		msg := fmt.Sprintf("State: %s", state)
		if info != nil {
			msg += fmt.Sprintf("\nMode: %s", info.Mode)
			if info.JoinURL != "" {
				msg += fmt.Sprintf("\nJoin URL: %s", info.JoinURL)
			}
			if info.JoinCommand != "" {
				msg += fmt.Sprintf("\nJoin command: %s", info.JoinCommand)
			}
			if info.WorkspacePath != "" {
				msg += fmt.Sprintf("\nWorkspace: %s", info.WorkspacePath)
			}
			if info.FileCount > 0 {
				msg += fmt.Sprintf("\nFiles synced: %d", info.FileCount)
			}
			if info.ReadOnly {
				msg += "\nRead-only: yes"
			}
			if len(info.RecentFiles) > 0 {
				msg += "\nRecent files:"
				for _, f := range info.RecentFiles {
					msg += fmt.Sprintf("\n  - %s", f)
				}
			}
			if info.LastError != "" {
				msg += fmt.Sprintf("\nLast error: %s", info.LastError)
			}
		}
		return mcp.NewToolResultText(msg), nil
	}
}

func handleStop(sm *SessionManager) func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if err := sm.Stop(); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText("Session stopped."), nil
	}
}
