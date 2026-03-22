package mcp

import (
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// MCPServer is the interface we need from the mcp-go server.
type MCPServer interface {
	AddTool(tool mcp.Tool, handler server.ToolHandlerFunc)
}

// Serve creates an MCP server with Shadow tools and runs it over stdio.
func Serve(version string) error {
	s := server.NewMCPServer(
		"shadow",
		version,
		server.WithToolCapabilities(false),
	)

	sm := NewSessionManager()
	RegisterTools(s, sm)

	return server.ServeStdio(s)
}
