package client

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/go-johnnyhe/shadow/internal/e2e"
	"github.com/go-johnnyhe/shadow/internal/protocol"
	"github.com/go-johnnyhe/shadow/internal/ui"
	"github.com/go-johnnyhe/shadow/internal/wsutil"
	"github.com/gorilla/websocket"
)

type Client struct {
	conn                  *wsutil.Peer
	codec                 *e2e.Codec
	baseDir               string
	singleFileRel         string
	outboundIgnore        *OutboundIgnore
	timer                 *time.Timer
	timerMutex            sync.Mutex
	isWritingReceivedFile atomic.Bool
	isHost                bool
	readOnlyJoinerMode    atomic.Bool
	lastHash              sync.Map
	readyCh               chan struct{}
	readyOnce             sync.Once
}

type Options struct {
	IsHost     bool
	E2EKey     string
	BaseDir    string
	SingleFile string
}

func NewClient(conn *websocket.Conn, opts ...Options) (*Client, error) {
	opt := Options{}
	if len(opts) > 0 {
		opt = opts[0]
	}

	codec, err := e2e.NewCodec(opt.E2EKey)
	if err != nil {
		return nil, err
	}

	baseDir := opt.BaseDir
	if strings.TrimSpace(baseDir) == "" {
		baseDir = "."
	}
	baseDirAbs, err := filepath.Abs(baseDir)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve base directory: %w", err)
	}

	singleFileRel := strings.TrimSpace(filepath.ToSlash(opt.SingleFile))
	if singleFileRel != "" {
		singleFileRel = path.Clean(singleFileRel)
		if singleFileRel == "." || singleFileRel == ".." || strings.HasPrefix(singleFileRel, "../") || strings.HasPrefix(singleFileRel, "/") {
			return nil, fmt.Errorf("invalid single-file path")
		}
	}

	c := &Client{
		conn:           wsutil.NewPeer(conn),
		codec:          codec,
		baseDir:        baseDirAbs,
		singleFileRel:  singleFileRel,
		outboundIgnore: NewOutboundIgnore(baseDirAbs),
		isHost:         opt.IsHost,
		readyCh:        make(chan struct{}),
	}
	if c.isHost {
		c.markReady()
	}
	return c, nil
}

func (c *Client) Start(ctx context.Context) {
	go c.readLoop()
	go func() {
		if !c.isHost {
			select {
			case <-c.readyCh:
			case <-time.After(1200 * time.Millisecond):
			}
		}
		c.monitorFiles(ctx)
	}()
}

func (c *Client) SendInitialSnapshot() (int, error) {
	sentCount := 0

	if c.singleFileRel != "" {
		if c.sendFile(filepath.Join(c.baseDir, filepath.FromSlash(c.singleFileRel)), false) {
			sentCount++
		}
		return sentCount, nil
	}

	walkErr := filepath.WalkDir(c.baseDir, func(currentPath string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if currentPath == c.baseDir {
			return nil
		}

		relPath, relErr := c.relativeProtocolPath(currentPath)
		if relErr != nil {
			return nil
		}

		if c.shouldIgnoreOutboundRel(relPath, d.IsDir()) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil || !info.Mode().IsRegular() {
			return nil
		}
		if c.sendFile(currentPath, false) {
			sentCount++
		}
		return nil
	})
	if walkErr != nil {
		return sentCount, walkErr
	}
	return sentCount, nil
}

func fileHash(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func (c *Client) SendFile(filePath string) {
	c.sendFile(filePath, true)
}

func (c *Client) sendFile(filePath string, verbose bool) bool {
	if c.readOnlyJoinerMode.Load() {
		return false
	}

	if c.isWritingReceivedFile.Load() {
		return false
	}

	relPath, err := c.relativeProtocolPath(filePath)
	if err != nil {
		return false
	}
	if c.shouldIgnoreOutboundRel(relPath, false) {
		return false
	}

	absPath := filepath.Join(c.baseDir, filepath.FromSlash(relPath))

	fileInfo, err := os.Stat(absPath)
	if err != nil || !fileInfo.Mode().IsRegular() {
		return false
	}
	if fileInfo.Size() > 10*1024*1024 {
		if verbose {
			sizeMB := float64(fileInfo.Size()) / (1024 * 1024)
			fmt.Println(ui.Dim(fmt.Sprintf("⊘ skipped %s (%.0fMB, exceeds 10MB limit)", relPath, sizeMB)))
		}
		return false
	}
	content, err := os.ReadFile(absPath)
	if err != nil {
		log.Println("error reading the file: ", err)
		return false
	}

	newHash := fileHash(content)

	if prevContent, ok := c.lastHash.Load(relPath); ok && prevContent.(string) == newHash {
		return false
	}

	c.lastHash.Store(relPath, newHash)
	encodedContent := base64.StdEncoding.EncodeToString(content)
	plaintextMessage := []byte(fmt.Sprintf("%s|%s", relPath, encodedContent))
	encryptedPayload, err := c.codec.Encrypt(plaintextMessage)
	if err != nil {
		log.Println("error encrypting the file: ", err)
		return false
	}
	message := fmt.Sprintf("%s|%s", protocol.EncryptedChannel, encryptedPayload)

	if err := c.conn.Write(websocket.TextMessage, []byte(message)); err != nil {
		log.Println("error writing the file: ", err)
		return false
	}

	if verbose {
		fmt.Printf("%s %s\n", ui.OutArrow("→"), relPath)
	}
	return true
}

func (c *Client) readLoop() {
	for {
		_, msg, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				fmt.Println(ui.Warn("⚠ connection lost"))
			}
			return
		}
		if len(msg) > 10*1024*1024 {
			sizeMB := float64(len(msg)) / (1024 * 1024)
			fmt.Println(ui.Dim(fmt.Sprintf("⊘ skipped incoming message (%.0fMB, exceeds 10MB limit)", sizeMB)))
			continue
		}

		parts := strings.SplitN(string(msg), "|", 2)
		if len(parts) != 2 {
			log.Printf("Received invalid message format: %s\n", string(msg))
			continue
		}

		if parts[0] == protocol.ControlChannel {
			readOnly, ok := protocol.ParseReadOnlyJoinersControl(parts[1])
			if ok && !c.isHost {
				c.readOnlyJoinerMode.Store(readOnly)
				if readOnly {
					fmt.Println(ui.Dim("read-only mode · local edits will not sync"))
				}
			}
			if peerCount, ok := protocol.ParsePeerCountControl(parts[1]); ok {
				// Subtract 1 because the server counts us as a client too.
				// The host also connects as a local client, so peers = total - 1.
				others := peerCount - 1
				if others == 1 {
					fmt.Println(ui.Dim("1 peer connected"))
				} else if others > 1 {
					fmt.Println(ui.Dim(fmt.Sprintf("%d peers connected", others)))
				} else {
					fmt.Println(ui.Dim("all peers disconnected"))
				}
			}
			c.markReady()
			continue
		}

		c.markReady()

		if parts[0] == protocol.EncryptedChannel {
			decryptedBytes, decryptErr := c.codec.Decrypt(parts[1])
			if decryptErr != nil {
				log.Printf("failed to decrypt E2E message: %v\n", decryptErr)
				continue
			}
			parts = strings.SplitN(string(decryptedBytes), "|", 2)
			if len(parts) != 2 {
				log.Printf("Received invalid decrypted message format: %s\n", string(decryptedBytes))
				continue
			}
		}

		relPath, pathErr := normalizeIncomingPath(parts[0])
		if pathErr != nil {
			log.Printf("invalid incoming path %q: %v\n", parts[0], pathErr)
			continue
		}
		if c.shouldIgnoreInboundRel(relPath) {
			continue
		}

		decodedContent, err := base64.StdEncoding.DecodeString(parts[1])
		if err != nil {
			log.Printf("error decoding content for %s: %v\n", relPath, err)
			continue
		}

		destPath := filepath.Join(c.baseDir, filepath.FromSlash(relPath))
		if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
			log.Printf("error creating parent directories for %s: %v\n", relPath, err)
			continue
		}

		c.isWritingReceivedFile.Store(true)
		func() {
			defer c.isWritingReceivedFile.Store(false)
			if err = os.WriteFile(destPath, decodedContent, 0644); err != nil {
				log.Printf("error writing this file: %s: %v\n", relPath, err)
			} else {
				fmt.Printf("%s %s\n", ui.InArrow("←"), relPath)
			}
		}()
		c.lastHash.Store(relPath, fileHash(decodedContent))
	}
}

func (c *Client) markReady() {
	c.readyOnce.Do(func() {
		close(c.readyCh)
	})
}

func (c *Client) monitorFiles(ctx context.Context) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Println("failed to create a watcher: ", err)
		return
	}

	go func() {
		<-ctx.Done()
		watcher.Close()
	}()

	go c.processFileEvents(ctx, watcher)

	if err := c.addWatchRecursive(watcher, c.baseDir); err != nil {
		fmt.Println("\nCannot watch this directory (filesystem issue)")
		fmt.Println("\nQuick fix — run these 2 commands:")
		fmt.Println("  $ mkdir -p /tmp/shadow && cd /tmp/shadow")
		fmt.Println("  $ shadow join <session-url>")
		fmt.Println("\nThis will start your session in a clean directory.")
		os.Exit(1)
	}

	fmt.Println(ui.Dim("watching for changes..."))
}

func (c *Client) addWatchRecursive(watcher *fsnotify.Watcher, root string) error {
	return filepath.WalkDir(root, func(currentPath string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}

		if currentPath != c.baseDir {
			relPath, err := c.relativeProtocolPath(currentPath)
			if err != nil {
				return nil
			}
			if c.shouldIgnoreOutboundRel(relPath, d.IsDir()) {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
		}

		if !d.IsDir() {
			return nil
		}

		if c.singleFileRel != "" && currentPath != c.baseDir {
			return filepath.SkipDir
		}

		if err := watcher.Add(currentPath); err != nil {
			log.Printf("failed to watch %s: %v", currentPath, err)
		}
		return nil
	})
}

func (c *Client) processFileEvents(ctx context.Context, watcher *fsnotify.Watcher) {
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-watcher.Events:
			if !ok {
				return
			}

			if event.Op&fsnotify.Create != 0 {
				if info, err := os.Stat(event.Name); err == nil && info.IsDir() {
					if err := c.addWatchRecursive(watcher, event.Name); err != nil {
						log.Printf("failed to recursively watch %s: %v", event.Name, err)
					}
					continue
				}
			}

			if event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename|fsnotify.Chmod) != 0 {
				c.handleFileEvent(event)
			}
		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			log.Println("error with watcher:", err)
		}
	}
}

func (c *Client) handleFileEvent(event fsnotify.Event) {
	filePath := event.Name

	if info, err := os.Stat(filePath); err == nil && info.IsDir() {
		return
	}

	base := filepath.Base(filePath)

	if strings.HasSuffix(base, ".tmp") {
		orig := strings.TrimSuffix(filePath, ".tmp")
		if _, err := os.Stat(orig); err == nil {
			filePath = orig
			base = filepath.Base(orig)
		} else {
			return
		}
	}

	if strings.HasSuffix(base, "~") {
		orig := strings.TrimSuffix(filePath, "~")
		if _, err := os.Stat(orig); err == nil {
			filePath = orig
			base = filepath.Base(orig)
		} else {
			return
		}
	}

	relPath, err := c.relativeProtocolPath(filePath)
	if err != nil {
		return
	}
	if c.shouldIgnoreOutboundRel(relPath, false) {
		return
	}

	c.timerMutex.Lock()
	if c.timer != nil {
		c.timer.Stop()
	}

	c.timer = time.AfterFunc(50*time.Millisecond, func() {
		c.SendFile(filePath)
	})
	c.timerMutex.Unlock()
}

func (c *Client) relativeProtocolPath(filePath string) (string, error) {
	absPath, err := filepath.Abs(filePath)
	if err != nil {
		return "", err
	}

	relPath, err := filepath.Rel(c.baseDir, absPath)
	if err != nil {
		return "", err
	}
	relSlash := path.Clean(filepath.ToSlash(relPath))
	if relSlash == "." || relSlash == ".." || strings.HasPrefix(relSlash, "../") || strings.HasPrefix(relSlash, "/") {
		return "", fmt.Errorf("path %q is outside base directory", filePath)
	}

	if c.singleFileRel != "" && relSlash != c.singleFileRel {
		return "", fmt.Errorf("path %q is outside file scope", filePath)
	}

	return relSlash, nil
}

func normalizeIncomingPath(rawPath string) (string, error) {
	slashPath := strings.ReplaceAll(strings.TrimSpace(rawPath), "\\", "/")
	cleanPath := path.Clean(slashPath)
	if cleanPath == "" || cleanPath == "." || cleanPath == ".." || strings.HasPrefix(cleanPath, "../") || strings.HasPrefix(cleanPath, "/") {
		return "", fmt.Errorf("unsafe path")
	}
	return cleanPath, nil
}

func (c *Client) shouldIgnoreOutboundRel(relPath string, isDir bool) bool {
	if c.outboundIgnore == nil {
		return hardcodedIgnore.MatchString(relPath)
	}
	return c.outboundIgnore.Match(relPath, isDir)
}

func (c *Client) shouldIgnoreInboundRel(relPath string) bool {
	return shouldIgnoreInbound(relPath)
}

