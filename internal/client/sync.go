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
	fileTimers            map[string]*time.Timer
	fileTimersMu          sync.Mutex
	snapshotMu            sync.Mutex
	isWritingReceivedFile atomic.Bool
	isHost                bool
	readOnlyJoinerMode    atomic.Bool
	connectedPeers        atomic.Int64
	lastHash              sync.Map
	readyCh               chan struct{}
	readyOnce             sync.Once
	onEvent               func(eventType, relPath, message string)
}

type Options struct {
	IsHost     bool
	E2EKey     string
	BaseDir    string
	SingleFile string
	OnEvent    func(eventType, relPath, message string)
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
		fileTimers:     make(map[string]*time.Timer),
		onEvent:        opt.OnEvent,
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
			case <-time.After(10 * time.Second):
				// Safety fallback: start monitoring even if control
				// message hasn't arrived (e.g. high-latency tunnel).
			case <-ctx.Done():
				return
			}
		}
		c.monitorFiles(ctx)
	}()
	go func() {
		<-ctx.Done()
		c.stopAllFileTimers()
	}()
}

func (c *Client) stopAllFileTimers() {
	c.fileTimersMu.Lock()
	defer c.fileTimersMu.Unlock()
	for _, t := range c.fileTimers {
		t.Stop()
	}
	c.fileTimers = make(map[string]*time.Timer)
}

func (c *Client) SendInitialSnapshot() (int, error) {
	return c.sendSnapshot(false)
}

// sendSnapshot walks the shared path and sends its current contents. A forced
// snapshot is used when a new peer joins: the normal hash deduplication cannot
// be used because that peer has not seen files sent before it connected.
func (c *Client) sendSnapshot(force bool) (int, error) {
	c.snapshotMu.Lock()
	defer c.snapshotMu.Unlock()

	sentCount := 0

	if c.singleFileRel != "" {
		if c.sendFile(filepath.Join(c.baseDir, filepath.FromSlash(c.singleFileRel)), false, force) {
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
		if c.sendFile(currentPath, false, force) {
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
	c.sendFile(filePath, true, false)
}

func (c *Client) sendFile(filePath string, verbose, force bool) bool {
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
			c.notifySkipped(relPath, sizeMB)
		}
		return false
	}
	content, err := os.ReadFile(absPath)
	if err != nil {
		log.Println("error reading the file: ", err)
		return false
	}

	newHash := fileHash(content)

	if !force {
		if prevContent, ok := c.lastHash.Load(relPath); ok && prevContent.(string) == newHash {
			return false
		}
	}

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
	c.lastHash.Store(relPath, newHash)

	if verbose {
		c.notifyFileSent(relPath, false)
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
		c.notifyFileSent(relPath, true)
	}
	return true
}

func (c *Client) readLoop() {
	for {
		_, msg, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsCloseError(err, websocket.CloseMessageTooBig) || strings.Contains(err.Error(), "read limit exceeded") {
				c.notifyWarning("⚠ incoming data exceeded transport limit")
				return
			}
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				c.notifyWarning("⚠ connection lost")
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
					c.notifyReadOnly()
				}
			}
			if peerCount, ok := protocol.ParsePeerCountControl(parts[1]); ok {
				others := peerCount - 1
				previous := c.connectedPeers.Swap(int64(others))
				c.notifyPeerCount(others)
				if c.isHost && others > int(previous) {
					go func() {
						if _, snapshotErr := c.sendSnapshot(true); snapshotErr != nil {
							log.Printf("failed to sync new peer: %v", snapshotErr)
						}
					}()
				}
			}
			c.markReady()
			continue
		}

		c.markReady()

		if parts[0] != protocol.EncryptedChannel {
			log.Printf("ignored message on unsupported channel %q\n", parts[0])
			continue
		}
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

		relPath, pathErr := normalizeIncomingPath(parts[0])
		if pathErr != nil {
			log.Printf("invalid incoming path %q: %v\n", parts[0], pathErr)
			continue
		}
		if c.shouldIgnoreInboundRel(relPath) {
			continue
		}

		destPath, destErr := secureIncomingDestination(c.baseDir, relPath)
		if destErr != nil {
			log.Printf("unsafe incoming destination %q: %v\n", relPath, destErr)
			c.notifyWarning(fmt.Sprintf("⚠ skipped unsafe path %s", relPath))
			continue
		}
		if parts[1] == deletePayloadMarker {
			if err := os.RemoveAll(destPath); err != nil && !os.IsNotExist(err) {
				log.Printf("error deleting %s: %v\n", relPath, err)
			} else {
				c.notifyFileReceived(relPath, true)
			}
			c.dropPathHashes(relPath)
			continue
		}

		decodedContent, err := decodeIncomingFileContent(parts[1])
		if err != nil {
			var tooLarge incomingFileTooLargeError
			if errors.As(err, &tooLarge) {
				sizeMB := float64(tooLarge.size) / (1024 * 1024)
				c.notifySkipped(relPath, sizeMB)
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
				// Nudge IDE file watchers (JetBrains VFS, etc.) that may miss
				// the atomic rename. A Chtimes on the final path emits a clean
				// metadata event on the real inode.
				now := time.Now()
				_ = os.Chtimes(destPath, now, now)
				c.notifyFileReceived(relPath, false)
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

// Output helpers — route to onEvent callback when set, else print to stdout.

func (c *Client) notifyFileSent(relPath string, deleted bool) {
	if c.onEvent != nil {
		c.onEvent("file_sent", relPath, relPath)
		return
	}
	if deleted {
		fmt.Printf("%s %s %s\n", ui.OutArrow("→"), relPath, ui.Dim("(deleted)"))
	} else {
		fmt.Printf("%s %s\n", ui.OutArrow("→"), relPath)
	}
}

func (c *Client) notifyFileReceived(relPath string, deleted bool) {
	if c.onEvent != nil {
		c.onEvent("file_received", relPath, relPath)
		return
	}
	if deleted {
		fmt.Printf("%s %s %s\n", ui.InArrow("←"), relPath, ui.Dim("(deleted)"))
	} else {
		fmt.Printf("%s %s\n", ui.InArrow("←"), relPath)
	}
}

func (c *Client) notifyReadOnly() {
	if c.onEvent != nil {
		c.onEvent("read_only", "", "Read-only mode active")
		return
	}
	fmt.Println(ui.Bold("read-only mode · local edits will not sync"))
}

func (c *Client) notifyPeerCount(others int) {
	if c.onEvent != nil {
		c.onEvent("peer_count", "", fmt.Sprintf("%d", others))
		return
	}
	if others == 1 {
		fmt.Println(ui.Dim("1 peer connected"))
	} else if others > 1 {
		fmt.Println(ui.Dim(fmt.Sprintf("%d peers connected", others)))
	} else {
		fmt.Println(ui.Dim("no peers connected"))
	}
}

func (c *Client) notifyWarning(msg string) {
	if c.onEvent != nil {
		c.onEvent("warning", "", msg)
		return
	}
	fmt.Println(ui.Warn(msg))
}

func (c *Client) notifySkipped(relPath string, sizeMB float64) {
	if c.onEvent != nil {
		c.onEvent("warning", relPath, fmt.Sprintf("skipped (%.0fMB, exceeds 10MB limit)", sizeMB))
		return
	}
	fmt.Println(ui.Dim(fmt.Sprintf("⊘ skipped %s (%.0fMB, exceeds 10MB limit)", relPath, sizeMB)))
}

func (c *Client) notifyInfo(msg string) {
	if c.onEvent != nil {
		return
	}
	fmt.Println(ui.Dim(msg))
}

func (c *Client) monitorFiles(ctx context.Context) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		msg := fmt.Sprintf("cannot create file watcher: %v", err)
		log.Println(msg)
		if c.onEvent != nil {
			c.onEvent("error", "", msg)
		}
		return
	}

	go func() {
		<-ctx.Done()
		watcher.Close()
	}()

	go c.processFileEvents(ctx, watcher)

	if err := c.addWatchRecursive(watcher, c.baseDir); err != nil {
		msg := fmt.Sprintf("cannot watch directory: %v", err)
		if c.onEvent != nil {
			c.onEvent("error", "", msg)
			return
		}
		fmt.Printf("\n%s\n", msg)
		fmt.Println("\nQuick fix, run these commands:")
		fmt.Println("  $ mkdir -p /tmp/shadow && cd /tmp/shadow")
		fmt.Println("  $ shadow join <session-url>")
		fmt.Println("\nThis will start your session in a clean directory.")
		os.Exit(1)
	}

	c.notifyInfo("watching for changes...")
}

func (c *Client) addWatchRecursive(watcher *fsnotify.Watcher, root string) error {
	cleanRoot := filepath.Clean(root)
	watchedAny := false
	watchedRoot := false
	var rootWatchErr error

	err := filepath.WalkDir(cleanRoot, func(currentPath string, d fs.DirEntry, walkErr error) error {
		currentPath = filepath.Clean(currentPath)
		if walkErr != nil {
			log.Printf("failed to inspect %s: %v", currentPath, walkErr)
			if currentPath == cleanRoot && rootWatchErr == nil {
				rootWatchErr = fmt.Errorf("cannot access %s: %w", currentPath, walkErr)
			}
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
			if currentPath == cleanRoot && rootWatchErr == nil {
				rootWatchErr = fmt.Errorf("cannot watch %s: %w", currentPath, err)
			}
			return nil
		}
		watchedAny = true
		if currentPath == cleanRoot {
			watchedRoot = true
		}
		return nil
	})
	if err != nil {
		return err
	}
	if !watchedRoot && rootWatchErr != nil {
		return rootWatchErr
	}
	if !watchedAny {
		if rootWatchErr != nil {
			return rootWatchErr
		}
		return fmt.Errorf("no watchable directories found under %s", cleanRoot)
	}
	return nil
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
			if c.onEvent != nil {
				c.onEvent("warning", "", fmt.Sprintf("file watcher error: %v", err))
			}
		}
	}
}

func (c *Client) scheduleFileTimer(relPath string, fn func()) {
	c.fileTimersMu.Lock()
	defer c.fileTimersMu.Unlock()
	if old, ok := c.fileTimers[relPath]; ok {
		old.Stop()
	}
	var t *time.Timer
	t = time.AfterFunc(50*time.Millisecond, func() {
		fn()
		c.fileTimersMu.Lock()
		if c.fileTimers[relPath] == t {
			delete(c.fileTimers, relPath)
		}
		c.fileTimersMu.Unlock()
	})
	c.fileTimers[relPath] = t
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

	c.scheduleFileTimer(relPath, func() { c.SendFile(filePath) })
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

	c.scheduleFileTimer(relPath, func() {
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

// secureIncomingDestination prevents an existing symlinked directory inside a
// join folder from redirecting writes or deletions outside that folder. The
// base directory itself is trusted because it is explicitly chosen by the user.
func secureIncomingDestination(baseDir, relPath string) (string, error) {
	parts := strings.Split(filepath.FromSlash(relPath), string(filepath.Separator))
	current := filepath.Clean(baseDir)
	for _, part := range parts[:len(parts)-1] {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			break
		}
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("parent directory is a symlink")
		}
		if !info.IsDir() {
			return "", fmt.Errorf("parent path is not a directory")
		}
	}
	return filepath.Join(baseDir, filepath.FromSlash(relPath)), nil
}

func atomicWriteFile(destPath string, data []byte, perm os.FileMode) error {
	targetPerm := perm

	if info, err := os.Lstat(destPath); err == nil {
		if info.Mode()&os.ModeSymlink == 0 {
			targetPerm = info.Mode().Perm()
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	tmpFile, err := os.CreateTemp(filepath.Dir(destPath), "."+filepath.Base(destPath)+".shadow_tmp_*.tmp")
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

	if err := os.Rename(tmpPath, destPath); err != nil {
		// Windows may reject rename-over-existing. Fallback keeps behavior working.
		if removeErr := os.Remove(destPath); removeErr == nil || errors.Is(removeErr, os.ErrNotExist) {
			if retryErr := os.Rename(tmpPath, destPath); retryErr == nil {
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
