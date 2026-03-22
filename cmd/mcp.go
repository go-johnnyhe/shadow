package cmd

import (
	shadowmcp "github.com/go-johnnyhe/shadow/mcp"
	"github.com/spf13/cobra"
)

var mcpCmd = &cobra.Command{
	Use:    "mcp",
	Short:  "Run the MCP server (for AI agent integration)",
	Hidden: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		return shadowmcp.Serve(Version)
	},
}

func init() {
	rootCmd.AddCommand(mcpCmd)
}
