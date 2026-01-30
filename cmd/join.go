/*
Copyright © 2025 NAME HERE <EMAIL ADDRESS>
*/
package cmd

import (
	"context"
	"fmt"
	"github.com/go-johnnyhe/shadow/internal/client"
	"github.com/gorilla/websocket"
	"github.com/spf13/cobra"
	"os"
	"os/signal"
	"strings"
	"syscall"
)

// joinCmd represents the join command
var joinCmd = &cobra.Command{
	Use:   "join <session-url>",
	Short: "Join an existing collaborative coding session",
	Long: `Join a collaborative coding session by connecting to the provided URL.

This will:
- Connect to the session via WebSocket
- Enable real-time file synchronization

Example:
  shadow join https://abc123.trycloudflare.com

The session URL comes from whoever ran 'shadow start'.`,
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("join called")
		if len(args) != 1 {
			fmt.Println("Error: this takes exactly one url")
			cmd.Usage()
			return
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		sessionUrl := args[0]
		wsURL := strings.Replace(sessionUrl, "https://", "wss://", 1)
		if !strings.HasSuffix(wsURL, "/ws") {
			wsURL = strings.TrimSuffix(wsURL, "/") + "/ws"
		}
		fmt.Println("Starting your mock interview session ...")
		conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
		if err != nil {
			fmt.Println("Error making connection", err)
			return
		}
		defer conn.Close()

		c := client.NewClient(conn)
		c.Start(ctx)

		<-ctx.Done()
		fmt.Println("")
		fmt.Println("Goodbye!")
	},
}

func init() {
	rootCmd.AddCommand(joinCmd)
}
