package cmd

import (
	"context"
	"fmt"
	"github.com/go-johnnyhe/shadow/internal/client"
	"github.com/go-johnnyhe/shadow/internal/tunnel"
	"github.com/go-johnnyhe/shadow/server"
	"github.com/gorilla/websocket"
	"github.com/spf13/cobra"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

var startPort int

// startCmd represents the start command
var startCmd = &cobra.Command{
	Use:   "start <file>",
	Short: "Start a collaborative coding session and share files",
	Long: `Start a new collaborative coding session with instant file sharing.

This command will:
- Launch a WebSocket server for real-time collaboration
- Create a secure tunnel using Cloudflared (no setup required)
- Share the current directory or specified files with anyone who joins the session
- Generate a shareable URL for your coding partner

Example:
  shadow start main.py              # Share a single file
  shadow start .                    # Share current directory

The generated URL can be shared with anyone - they can join using:
  shadow join <your-session-url>

Perfect for mock interviews, pair programming, and collaborative debugging.`,
	Run: func(cmd *cobra.Command, args []string) {
		if len(args) != 1 {
			fmt.Println("Error: this takes exactly one file")
			cmd.Usage()
			return
		}

		fileName := args[0]

		if _, err := os.Stat(fileName); os.IsNotExist(err) {
			if f, err := os.Create(fileName); err != nil {
				fmt.Printf("failed to create %s: %v\n", fileName, err)
				return
			} else {
				f.Close()
				fmt.Printf("Created %s (empty file)\n", fileName)
			}
		} else if err != nil {
			fmt.Printf("error checking %s: %v\n", fileName, err)
			return
		}

		// fmt.Printf("Starting the mock session with %s\n", fileName)

		// Find available port
		actualPort, listener, err := findAvailablePort(startPort)
		if err != nil {
			fmt.Printf("Failed to find available port: %v\n", err)
			os.Exit(1)
		}

		if actualPort != startPort {
			fmt.Printf("Port %d was in use, using port %d instead\n", startPort, actualPort)
		}

		// Create a context to link with a command line process so that when you stop, we know where to exit
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()

		// start server in go routine
		http.HandleFunc("/ws", server.StartServer)
		srv := &http.Server{}
		go func() {
			if err := srv.Serve(listener); err != http.ErrServerClosed {
				fmt.Printf("Server failed: %v\n", err)
				os.Exit(1)
			}
		}()

		// give server a moment to start
		time.Sleep(1 * time.Second)

		// fmt.Println("Connecting...")
		tunnelURL, err := tunnel.StartCloudflaredTunnel(ctx, actualPort)
		if err != nil {
			fmt.Printf("Failed to create tunnel: %v\n", err)
			fmt.Printf("Server is running locally on localhost:%d\n", actualPort)
			return
		}

		fmt.Printf("\n✅ Shadowing %s\n", fileName)
		fmt.Println("")
		fmt.Printf("Share this command with your partner:\n")

		// Bold the command for better visibility
		if os.Getenv("TERM") != "dumb" && os.Getenv("NO_COLOR") == "" {
			fmt.Printf("\n  \033[1mshadow join %s\033[0m\n", tunnelURL)
		} else {
			fmt.Printf("\n  shadow join %s\n", tunnelURL)
		}

		// let the starter user connect as a client too
		go func(ctx context.Context, port int) {
			time.Sleep(500 * time.Millisecond)
			conn, _, err := websocket.DefaultDialer.Dial(fmt.Sprintf("ws://localhost:%d/ws", port), nil)
			if err != nil {
				fmt.Println("Error connecting to websocket: ", err)
				return
			}
			defer conn.Close()

			c := client.NewClient(conn)
			c.SendFile(fileName)
			c.Start(ctx)
			<-ctx.Done()
		}(ctx, actualPort)

		<-ctx.Done()
		srv.Shutdown(context.Background())
		time.Sleep(100 * time.Millisecond)
		fmt.Println("")
		fmt.Println("Goodbye!")

	},
}

func findAvailablePort(startPort int) (int, net.Listener, error) {
	for port := startPort; port <= startPort+100; port++ {
		listener, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
		if err == nil {
			return port, listener, nil
		}
	}
	return 0, nil, fmt.Errorf("no available ports found between %d and %d", startPort, startPort+100)
}

func init() {
	rootCmd.AddCommand(startCmd)
	startCmd.Flags().IntVarP(&startPort, "port", "p", 8080, "Port to run the server on (will auto-increment if in use)")
}
