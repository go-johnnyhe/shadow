package client

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
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

const (
	maxSyncedFileBytes      = 10 * 1024 * 1024
	maxIncomingMessageBytes = 20 * 1024 * 1024
	deletePayloadMarker     = "__shadow_delete__"
)

type incomingFileTooLargeError struct {
	size int
}

func (e incomingFileTooLargeError) Error() string {
	return fmt.Sprintf("decoded file payload exceeds %d-byte limit", maxSyncedFileBytes)
}

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
	conn.SetReadLimit(maxIncomingMessageBytes)

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
	if fileInfo.Size() > maxSyncedFileBytes {
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

func (c *Client) sendDelete(relPath string, verbose bool) bool {
	if c.readOnlyJoinerMode.Load() {
		return false
	}
	if c.isWritingReceivedFile.Load() {
		return false
	}
	if c.shouldIgnoreOutboundRel(relPath, false) {
		return false
	}

	plaintextMessage := []byte(fmt.Sprintf("%s|%s", relPath, deletePayloadMarker))
	encryptedPayload, err := c.codec.Encrypt(plaintextMessage)
	if err != nil {
		log.Println("error encrypting delete message: ", err)
		return false
	}
	message := fmt.Sprintf("%s|%s", protocol.EncryptedChannel, encryptedPayload)

	if err := c.conn.Write(websocket.TextMessage, []byte(message)); err != nil {
		log.Println("error writing delete message: ", err)
		return false
	}
	c.dropPathHashes(relPath)

	if verbose {
		fmt.Printf("%s %s %s\n", ui.OutArrow("→"), relPath, ui.Dim("(deleted)"))
	}
	return true
}

func (c *Client) readLoop() {
	for {
		_, msg, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsCloseError(err, websocket.CloseMessageTooBig) || strings.Contains(err.Error(), "read limit exceeded") {
				fmt.Println(ui.Warn("⚠ incoming data exceeded transport limit"))
				return
			}
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				fmt.Println(ui.Warn("⚠ connection lost"))
			}
			return
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

		destPath := filepath.Join(c.baseDir, filepath.FromSlash(relPath))
		if parts[1] == deletePayloadMarker {
			if err := os.RemoveAll(destPath); err != nil && !os.IsNotExist(err) {
				log.Printf("error deleting %s: %v\n", relPath, err)
			} else {
				fmt.Printf("%s %s %s\n", ui.InArrow("←"), relPath, ui.Dim("(deleted)"))
			}
			c.dropPathHashes(relPath)
			continue
		}

		decodedContent, err := decodeIncomingFileContent(parts[1])
		if err != nil {
			var tooLarge incomingFileTooLargeError
			if errors.As(err, &tooLarge) {
				sizeMB := float64(tooLarge.size) / (1024 * 1024)
				fmt.Println(ui.Dim(fmt.Sprintf("⊘ skipped incoming %s (%.0fMB, exceeds 10MB limit)", relPath, sizeMB)))
				continue
			}
			log.Printf("error decoding content for %s: %v\n", relPath, err)
			continue
		}

		if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
			log.Printf("error creating parent directories for %s: %v\n", relPath, err)
			continue
		}

		c.isWritingReceivedFile.Store(true)
		func() {
			defer c.isWritingReceivedFile.Store(false)
			if err = atomicWriteFile(destPath, decodedContent, 0644); err != nil {
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

			if event.Op&(fsnotify.Remove|fsnotify.Rename) != 0 {
				c.handleDeleteEvent(event)
			}

			if event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Chmod) != 0 {
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

func (c *Client) handleDeleteEvent(event fsnotify.Event) {
	filePath := event.Name
	wasRename := event.Op&fsnotify.Rename != 0
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
		// Rename often emits delete before create; avoid false delete if file reappears.
		if _, statErr := os.Stat(filePath); statErr == nil {
			c.SendFile(filePath)
			return
		}
		c.sendDelete(relPath, true)
		if wasRename {
			if _, snapshotErr := c.SendInitialSnapshot(); snapshotErr != nil {
				log.Printf("failed to rescan after rename %s: %v", relPath, snapshotErr)
			}
		}
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

func atomicWriteFile(destPath string, data []byte, perm os.FileMode) error {
	writePath := destPath
	targetPerm := perm

	if info, err := os.Lstat(destPath); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			linkTarget, readErr := os.Readlink(destPath)
			if readErr != nil {
				return readErr
			}
			if filepath.IsAbs(linkTarget) {
				writePath = linkTarget
			} else {
				writePath = filepath.Join(filepath.Dir(destPath), linkTarget)
			}
			if targetInfo, statErr := os.Stat(writePath); statErr == nil {
				targetPerm = targetInfo.Mode().Perm()
			}
		} else {
			targetPerm = info.Mode().Perm()
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	tmpFile, err := os.CreateTemp(filepath.Dir(writePath), "."+filepath.Base(writePath)+".shadow_tmp_*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmpFile.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()

	if err := tmpFile.Chmod(targetPerm); err != nil {
		_ = tmpFile.Close()
		return err
	}
	if _, err := tmpFile.Write(data); err != nil {
		_ = tmpFile.Close()
		return err
	}
	if err := tmpFile.Close(); err != nil {
		return err
	}

	if err := os.Rename(tmpPath, writePath); err != nil {
		// Windows may reject rename-over-existing. Fallback keeps behavior working.
		if removeErr := os.Remove(writePath); removeErr == nil || errors.Is(removeErr, os.ErrNotExist) {
			if retryErr := os.Rename(tmpPath, writePath); retryErr == nil {
				cleanup = false
				return nil
			}
		}
		return err
	}
	cleanup = false
	return nil
}

func decodeIncomingFileContent(encoded string) ([]byte, error) {
	decodedContent, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, err
	}
	if len(decodedContent) > maxSyncedFileBytes {
		return nil, incomingFileTooLargeError{size: len(decodedContent)}
	}
	return decodedContent, nil
}

func (c *Client) dropPathHashes(relPath string) {
	c.lastHash.Delete(relPath)
	prefix := relPath + "/"
	c.lastHash.Range(func(key, _ any) bool {
		pathKey, ok := key.(string)
		if ok && strings.HasPrefix(pathKey, prefix) {
			c.lastHash.Delete(pathKey)
		}
		return true
	})
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
