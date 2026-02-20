/*
Copyright © 2025 NAME HERE <EMAIL ADDRESS>
*/
package cmd

import (
	"fmt"
	"github.com/spf13/cobra"
)

var joinKey string

// joinCmd represents the join command
var joinCmd = &cobra.Command{
	Use:   "join <session-url>",
	Short: "Join an existing collaborative coding session",
	Long: `Join a collaborative coding session by connecting to the provided URL.

This will:
- Connect to the session via WebSocket
- Enable real-time file synchronization

Example:
  shadow join 'https://abc123.trycloudflare.com#<e2e-key>'

The session URL comes from whoever ran 'shadow start'.`,
	Run: func(cmd *cobra.Command, args []string) {
		if len(args) != 1 {
			fmt.Println("Error: this takes exactly one url")
			cmd.Usage()
			return
		}

		err := runJoin(JoinOptions{
			SessionURL: args[0],
			E2EKey:     joinKey,
		})
		if err != nil {
			fmt.Printf("Error: %v\n", err)
		}
	},
}

func init() {
	rootCmd.AddCommand(joinCmd)
	joinCmd.Flags().StringVar(&joinKey, "key", "", "E2E share key (optional if included in URL fragment)")
}
