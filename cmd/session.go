package cmd

import (
	"context"
	"fmt"
	"github.com/go-johnnyhe/shadow/internal/client"
	"github.com/go-johnnyhe/shadow/internal/e2e"
	"github.com/go-johnnyhe/shadow/internal/tunnel"
	"github.com/go-johnnyhe/shadow/server"
	"github.com/gorilla/websocket"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

type StartOptions struct {
	Path            string
	Port            int
	E2EKey          string
	ReadOnlyJoiners bool
	Force           bool
}

type JoinOptions struct {
	SessionURL string
	E2EKey     string
}

func runStart(opts StartOptions) error {
	if opts.Path == "" {
		opts.Path = "."
	}
	opts.E2EKey = strings.TrimSpace(opts.E2EKey)
	if opts.E2EKey == "" {
		generatedKey, err := e2e.GenerateShareKey()
		if err != nil {
			return err
		}
		opts.E2EKey = generatedKey
	}

	if stat, err := os.Stat(opts.Path); os.IsNotExist(err) {
		f, createErr := os.Create(opts.Path)
		if createErr != nil {
			return fmt.Errorf("failed to create %s: %w", opts.Path, createErr)
		}
		f.Close()
		fmt.Printf("Created %s (empty file)\n", opts.Path)
	} else if err != nil {
		return fmt.Errorf("error checking %s: %w", opts.Path, err)
	} else if stat.IsDir() && opts.Path != "." {
		fmt.Printf("Sharing directory: %s\n", opts.Path)
	}

	absSharePath, err := filepath.Abs(opts.Path)
	if err != nil {
		return fmt.Errorf("failed to resolve share path: %w", err)
	}
	shareInfo, err := os.Stat(absSharePath)
	if err != nil {
		return fmt.Errorf("failed to stat share path: %w", err)
	}

	shareBaseDir := absSharePath
	shareSingleFile := ""
	if !shareInfo.IsDir() {
		shareBaseDir = filepath.Dir(absSharePath)
		shareSingleFile = filepath.Base(absSharePath)
	}
	if err := validateShareBaseDir(shareBaseDir); err != nil {
		return err
	}
	if shareSingleFile == "" {
		outboundIgnore := client.NewOutboundIgnore(shareBaseDir)
		estimate, err := estimateShareSnapshot(shareBaseDir, outboundIgnore)
		if err != nil {
			return fmt.Errorf("failed to inspect share directory: %w", err)
		}
		if shouldPromptLargeShare(estimate, opts.Force) {
			confirmed, err := promptLargeShareConfirmation(os.Stdin, os.Stdout, estimate)
			if err != nil {
				return err
			}
			if !confirmed {
				return fmt.Errorf("start canceled")
			}
		}
	}

	actualPort, listener, err := findAvailablePort(opts.Port)
	if err != nil {
		return fmt.Errorf("failed to find available port: %w", err)
	}
	if actualPort != opts.Port {
		fmt.Printf("Port %d was in use, using port %d instead\n", opts.Port, actualPort)
	}

	server.SetReadOnlyJoiners(opts.ReadOnlyJoiners)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", server.StartServer)
	srv := &http.Server{Handler: mux}
	go func() {
		if serveErr := srv.Serve(listener); serveErr != http.ErrServerClosed {
			fmt.Printf("Server failed: %v\n", serveErr)
			os.Exit(1)
		}
	}()

	time.Sleep(1 * time.Second)

	tunnelURL, err := tunnel.StartCloudflaredTunnel(ctx, actualPort)
	if err != nil {
		return fmt.Errorf("failed to create tunnel: %w (server is running locally on localhost:%d)", err, actualPort)
	}
	shareJoinURL, err := appendURLFragment(tunnelURL, opts.E2EKey)
	if err != nil {
		return err
	}

	fmt.Printf("\n✅ Shadowing %s\n\n", opts.Path)
	fmt.Println("Share this command with your partner:")
	quotedJoinURL := fmt.Sprintf("%q", shareJoinURL)
	if os.Getenv("TERM") != "dumb" && os.Getenv("NO_COLOR") == "" {
		fmt.Printf("\n  \033[1mshadow join %s\033[0m\n", quotedJoinURL)
	} else {
		fmt.Printf("\n  shadow join %s\n", quotedJoinURL)
	}
	fmt.Println("\nE2E: file payloads are encrypted client-to-client.")
	if opts.ReadOnlyJoiners {
		fmt.Println("Mode: joiners are read-only. Host edits continue syncing.")
	}

	go func(runCtx context.Context, port int) {
		time.Sleep(500 * time.Millisecond)
		conn, _, dialErr := websocket.DefaultDialer.Dial(fmt.Sprintf("ws://localhost:%d/ws", port), nil)
		if dialErr != nil {
			fmt.Println("Error connecting to websocket:", dialErr)
			return
		}
		defer conn.Close()

		c, clientErr := client.NewClient(conn, client.Options{
			IsHost:     true,
			E2EKey:     opts.E2EKey,
			BaseDir:    shareBaseDir,
			SingleFile: shareSingleFile,
		})
		if clientErr != nil {
			fmt.Println("Error initializing E2E client:", clientErr)
			return
		}
		c.Start(runCtx)
		count, snapshotErr := c.SendInitialSnapshot()
		if snapshotErr != nil {
			fmt.Println("Error sending initial snapshot:", snapshotErr)
		} else if count > 0 {
			fmt.Printf("Initial snapshot sent (%d files)\n", count)
		}
		<-runCtx.Done()
	}(ctx, actualPort)

	<-ctx.Done()
	srv.Shutdown(context.Background())
	time.Sleep(100 * time.Millisecond)
	fmt.Println("\nGoodbye!")
	return nil
}

func runJoin(opts JoinOptions) error {
	if opts.SessionURL == "" {
		return fmt.Errorf("session URL is required")
	}
	wsURL, keyFromURL, err := normalizeSessionWSURL(opts.SessionURL)
	if err != nil {
		return err
	}
	joinKey := strings.TrimSpace(opts.E2EKey)
	if joinKey == "" {
		joinKey = keyFromURL
	}
	if joinKey == "" {
		return fmt.Errorf("missing E2E key (use URL fragment like #<key> or pass --key)")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	fmt.Println("Starting your mock interview session ...")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		return fmt.Errorf("error making connection: %w", err)
	}
	defer conn.Close()

	c, err := client.NewClient(conn, client.Options{
		E2EKey: joinKey,
	})
	if err != nil {
		return fmt.Errorf("error initializing E2E client: %w", err)
	}
	c.Start(ctx)

	<-ctx.Done()
	fmt.Println("\nGoodbye!")
	return nil
}

func normalizeSessionWSURL(rawURL string) (string, string, error) {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return "", "", fmt.Errorf("session URL is required")
	}

	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", "", fmt.Errorf("invalid session URL: %w", err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return "", "", fmt.Errorf("invalid session URL: expected full URL with scheme and host")
	}

	keyFromURL := strings.TrimSpace(parsed.Fragment)
	parsed.Fragment = ""

	switch parsed.Scheme {
	case "https":
		parsed.Scheme = "wss"
	case "http":
		parsed.Scheme = "ws"
	case "wss", "ws":
	default:
		return "", "", fmt.Errorf("unsupported URL scheme %q", parsed.Scheme)
	}

	if parsed.Path == "" || parsed.Path == "/" {
		parsed.Path = "/ws"
	} else if !strings.HasSuffix(parsed.Path, "/ws") {
		parsed.Path = strings.TrimSuffix(parsed.Path, "/") + "/ws"
	}

	return parsed.String(), keyFromURL, nil
}

func appendURLFragment(rawURL, fragment string) (string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("failed to build share URL with E2E key: %w", err)
	}
	parsed.Fragment = fragment
	return parsed.String(), nil
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
