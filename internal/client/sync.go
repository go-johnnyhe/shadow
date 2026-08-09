package client

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path"
	"path/filepath"
	"sort"
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
	missingState            = "missing"
	directoryState          = "directory"
	otherState              = "other"
	conflictDirectory       = ".shadow-conflicts"
	renameRescanDelay       = 100 * time.Millisecond
	maxProtocolPathBytes    = 4096
	maxQueuedSnapshots      = 4
)

type Client struct {
	conn               *wsutil.Peer
	codec              *e2e.Codec
	baseDir            string
	singleFileRel      string
	singleFileMu       sync.RWMutex
	outboundIgnore     *OutboundIgnore
	fileTimers         map[string]*time.Timer
	fileTimersMu       sync.Mutex
	snapshotMu         sync.Mutex
	outboundMu         sync.Mutex
	renameRescanTimer  *time.Timer
	renameRescanMu     sync.Mutex
	rescan             func()
	isHost             bool
	readOnlyJoinerMode atomic.Bool
	syncReady          atomic.Bool
	connectedPeers     atomic.Int64
	lastHash           sync.Map
	pendingMu          sync.Mutex
	pending            map[string][]pendingOperation
	clientID           string
	nextOperation      atomic.Uint64
	lastSequence       atomic.Uint64
	readyCh            chan struct{}
	readyOnce          sync.Once
	watcherReadyCh     chan struct{}
	watcherReadyOnce   sync.Once
	snapshotRequests   chan string
	manifestReceived   bool
	doneCh             chan struct{}
	doneOnce           sync.Once
	stopping           atomic.Bool
	onEvent            func(eventType, relPath, message string)
}

type pendingOperation struct {
	id           string
	desiredState string
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
	clientID, err := e2e.GenerateShareKey()
	if err != nil {
		return nil, fmt.Errorf("failed to create client identity: %w", err)
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
		conn:             wsutil.NewPeer(conn),
		codec:            codec,
		baseDir:          baseDirAbs,
		singleFileRel:    singleFileRel,
		outboundIgnore:   NewOutboundIgnore(baseDirAbs),
		isHost:           opt.IsHost,
		clientID:         clientID,
		readyCh:          make(chan struct{}),
		watcherReadyCh:   make(chan struct{}),
		snapshotRequests: make(chan string, maxQueuedSnapshots),
		doneCh:           make(chan struct{}),
		fileTimers:       make(map[string]*time.Timer),
		pending:          make(map[string][]pendingOperation),
		onEvent:          opt.OnEvent,
	}
	c.rescan = func() {
		if _, snapshotErr := c.SendInitialSnapshot(); snapshotErr != nil {
			log.Printf("failed to rescan after rename: %v", snapshotErr)
		}
	}
	if c.isHost {
		c.syncReady.Store(true)
		c.markReady()
	}
	return c, nil
}

func (c *Client) Start(ctx context.Context) {
	go c.readLoop()
	go c.monitorFiles(ctx)
	if c.isHost {
		go c.processSnapshotRequests()
	}
	go func() {
		<-ctx.Done()
		c.stopping.Store(true)
		c.stopAllFileTimers()
		c.outboundIgnore.Close()
		_ = c.conn.Close()
	}()
}

func (c *Client) processSnapshotRequests() {
	for {
		select {
		case target := <-c.snapshotRequests:
			if _, err := c.sendSnapshot(true, target); err != nil {
				log.Printf("failed to sync new peer: %v", err)
			}
		case <-c.doneCh:
			return
		}
	}
}

func (c *Client) WaitReady(ctx context.Context) error {
	select {
	case <-c.readyCh:
		return nil
	case <-c.doneCh:
		return fmt.Errorf("Disconnected")
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Client) Done() <-chan struct{} {
	return c.doneCh
}

func (c *Client) stopAllFileTimers() {
	c.fileTimersMu.Lock()
	for _, t := range c.fileTimers {
		t.Stop()
	}
	c.fileTimers = make(map[string]*time.Timer)
	c.fileTimersMu.Unlock()

	c.renameRescanMu.Lock()
	if c.renameRescanTimer != nil {
		c.renameRescanTimer.Stop()
		c.renameRescanTimer = nil
	}
	c.renameRescanMu.Unlock()
}

func (c *Client) SendInitialSnapshot() (int, error) {
	return c.sendSnapshot(false, "")
}

// sendSnapshot walks the shared path and sends its current contents. A target
// identifies a new peer that is receiving an isolated bootstrap snapshot.
func (c *Client) sendSnapshot(force bool, target string) (int, error) {
	c.snapshotMu.Lock()
	defer c.snapshotMu.Unlock()
	c.outboundMu.Lock()
	defer c.outboundMu.Unlock()

	sentCount := 0
	singleFileRel := c.singleFileScope()
	if singleFileRel != "" {
		manifestPaths := make([]string, 0, 1)
		if target != "" {
			if _, err := os.Lstat(filepath.Join(c.baseDir, filepath.FromSlash(singleFileRel))); err == nil {
				manifestPaths = append(manifestPaths, singleFileRel)
			}
			if err := c.sendBootstrapManifestUnlocked(target, manifestPaths, nil); err != nil {
				return sentCount, err
			}
		}
		if c.sendFileUnlocked(filepath.Join(c.baseDir, filepath.FromSlash(singleFileRel)), false, force, target) {
			sentCount++
		}
		if target != "" {
			if err := c.conn.Write(websocket.TextMessage, protocol.EncodeSyncDone(target)); err != nil {
				return sentCount, err
			}
		}
		return sentCount, nil
	}
	if target != "" {
		manifestPaths, directories, manifestErr := c.snapshotManifest()
		if manifestErr != nil {
			return sentCount, manifestErr
		}
		if err := c.sendBootstrapManifestUnlocked(target, manifestPaths, directories); err != nil {
			return sentCount, err
		}
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
			c.lastHash.Store(relPath, directoryState)
			return nil
		}
		info, err := d.Info()
		if err != nil || !info.Mode().IsRegular() {
			return nil
		}
		if c.sendFileUnlocked(currentPath, false, force, target) {
			sentCount++
		}
		return nil
	})
	if walkErr != nil {
		return sentCount, walkErr
	}
	if target != "" {
		if err := c.conn.Write(websocket.TextMessage, protocol.EncodeSyncDone(target)); err != nil {
			return sentCount, err
		}
	}
	return sentCount, nil
}

func (c *Client) snapshotManifest() ([]string, []string, error) {
	paths := make([]string, 0)
	directories := make([]string, 0)
	err := filepath.WalkDir(c.baseDir, func(currentPath string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil || currentPath == c.baseDir {
			return nil
		}
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
		info, err := d.Info()
		if err != nil || (!d.IsDir() && !info.Mode().IsRegular()) {
			return nil
		}
		paths = append(paths, relPath)
		if d.IsDir() {
			directories = append(directories, relPath)
		}
		return nil
	})
	return paths, directories, err
}

func (c *Client) sendBootstrapManifestUnlocked(target string, paths, directories []string) error {
	plaintext, err := protocol.EncodeBootstrapManifest(paths, directories, c.singleFileScope())
	if err != nil {
		return err
	}
	encrypted, err := c.codec.Encrypt(plaintext)
	if err != nil {
		return err
	}
	return c.conn.Write(websocket.TextMessage, protocol.EncodeTargetedEncrypted(target, encrypted))
}

func fileHash(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func (c *Client) SendFile(filePath string) {
	c.outboundMu.Lock()
	defer c.outboundMu.Unlock()
	c.sendFileUnlocked(filePath, true, false, "")
}

func (c *Client) sendFileUnlocked(filePath string, verbose, force bool, target string) bool {
	if (target == "" && !c.syncReady.Load()) || c.readOnlyJoinerMode.Load() {
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

	if !force && c.latestPathState(relPath) == newHash {
		return false
	}

	operation := protocol.SyncOperation{
		ID:          c.nextOperationID(),
		Path:        relPath,
		BaseState:   c.latestPathState(relPath),
		DesiredHash: newHash,
		Content:     content,
	}
	plaintextMessage, err := protocol.EncodeSyncOperation(operation)
	if err != nil {
		log.Println("error encoding the file: ", err)
		return false
	}
	encryptedPayload, err := c.codec.Encrypt(plaintextMessage)
	if err != nil {
		log.Println("error encrypting the file: ", err)
		return false
	}
	message := protocol.EncodeEncrypted(encryptedPayload)
	if target != "" {
		message = protocol.EncodeTargetedEncrypted(target, encryptedPayload)
	} else {
		c.addPending(relPath, pendingOperation{id: operation.ID, desiredState: newHash})
	}

	if err := c.conn.Write(websocket.TextMessage, message); err != nil {
		if target == "" {
			c.removePending(relPath, operation.ID)
		}
		log.Println("error writing the file: ", err)
		return false
	}

	if verbose {
		c.notifyFileSent(relPath, false)
	}
	return true
}

func (c *Client) sendDelete(relPath string, verbose bool) bool {
	c.outboundMu.Lock()
	defer c.outboundMu.Unlock()
	return c.sendDeleteUnlocked(relPath, verbose)
}

func (c *Client) sendDeleteUnlocked(relPath string, verbose bool) bool {
	if !c.syncReady.Load() || c.readOnlyJoinerMode.Load() {
		return false
	}
	if c.shouldIgnoreOutboundRel(relPath, false) {
		return false
	}
	if c.latestPathState(relPath) == missingState {
		return false
	}

	operation := protocol.SyncOperation{
		ID:          c.nextOperationID(),
		Path:        relPath,
		BaseState:   c.latestPathState(relPath),
		DesiredHash: missingState,
		Delete:      true,
	}
	plaintextMessage, err := protocol.EncodeSyncOperation(operation)
	if err != nil {
		log.Println("error encoding delete message: ", err)
		return false
	}
	encryptedPayload, err := c.codec.Encrypt(plaintextMessage)
	if err != nil {
		log.Println("error encrypting delete message: ", err)
		return false
	}
	message := protocol.EncodeEncrypted(encryptedPayload)
	c.addPending(relPath, pendingOperation{id: operation.ID, desiredState: missingState})

	if err := c.conn.Write(websocket.TextMessage, message); err != nil {
		c.removePending(relPath, operation.ID)
		log.Println("error writing delete message: ", err)
		return false
	}

	if verbose {
		c.notifyFileSent(relPath, true)
	}
	return true
}

func (c *Client) readLoop() {
	defer c.doneOnce.Do(func() { close(c.doneCh) })
	defer c.conn.Close()
	for {
		_, message, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsCloseError(err, websocket.CloseMessageTooBig) || strings.Contains(err.Error(), "read limit exceeded") {
				c.notifyWarning("⚠ incoming data exceeded transport limit")
				return
			}
			if !c.stopping.Load() {
				c.notifyDisconnected()
			}
			return
		}

		parts := strings.SplitN(string(message), "|", 2)
		if len(parts) != 2 {
			log.Printf("received invalid message format\n")
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
				c.connectedPeers.Store(int64(others))
				c.notifyPeerCount(others)
			}
			if targetID, ok := protocol.ParseSyncRequestControl(parts[1]); ok && c.isHost {
				select {
				case c.snapshotRequests <- targetID:
				default:
					log.Printf("ignored sync request for peer %s: snapshot queue is full", targetID)
				}
			}
			if baseline, ok := protocol.ParseSyncBaselineControl(parts[1]); ok && !c.isHost && !c.syncReady.Load() {
				c.lastSequence.Store(baseline)
			}
			if protocol.ParseSyncCompleteControl(parts[1]) && !c.isHost {
				if !c.manifestReceived {
					c.notifyDisconnected()
					return
				}
				<-c.watcherReadyCh
				c.syncReady.Store(true)
				if _, snapshotErr := c.SendInitialSnapshot(); snapshotErr != nil {
					log.Printf("failed to rescan after bootstrap: %v", snapshotErr)
					c.notifyDisconnected()
					return
				}
				c.markReady()
			}
			continue
		}

		if sequence, encryptedPayload, ok := protocol.ParseOrderedEncrypted(message); ok {
			previous := c.lastSequence.Load()
			if sequence != previous+1 {
				log.Printf("invalid operation sequence: got %d after %d", sequence, previous)
				c.notifyDisconnected()
				return
			}
			if err := c.applyEncryptedOperation(encryptedPayload, false); err != nil {
				log.Printf("failed to apply operation %d: %v", sequence, err)
				c.notifyDisconnected()
				return
			}
			c.lastSequence.Store(sequence)
			continue
		}
		if encryptedPayload, ok := protocol.ParseBootstrapEncrypted(message); ok {
			if err := c.applyEncryptedOperation(encryptedPayload, true); err != nil {
				log.Printf("failed to apply bootstrap: %v", err)
				c.notifyDisconnected()
				return
			}
			continue
		}
		log.Printf("ignored message on unsupported channel %q\n", parts[0])
	}
}

func (c *Client) applyEncryptedOperation(encryptedPayload string, bootstrap bool) error {
	c.outboundMu.Lock()
	defer c.outboundMu.Unlock()

	decrypted, err := c.codec.Decrypt(encryptedPayload)
	if err != nil {
		return fmt.Errorf("decrypt: %w", err)
	}
	if bootstrap {
		manifest, isManifest, manifestErr := protocol.DecodeBootstrapManifest(decrypted)
		if manifestErr != nil {
			return manifestErr
		}
		if isManifest {
			if c.manifestReceived {
				return fmt.Errorf("duplicate bootstrap manifest")
			}
			if err := c.applyBootstrapManifest(manifest); err != nil {
				return err
			}
			c.manifestReceived = true
			return nil
		}
	}
	operation, err := protocol.DecodeSyncOperation(decrypted)
	if err != nil {
		return err
	}
	relPath, err := normalizeIncomingPath(operation.Path)
	if err != nil {
		return err
	}
	if c.shouldIgnoreInboundRel(relPath) {
		return fmt.Errorf("operation uses an ignored path")
	}
	if singleFileRel := c.singleFileScope(); singleFileRel != "" && relPath != singleFileRel {
		return fmt.Errorf("operation is outside file scope")
	}
	if !validPathState(operation.BaseState) || !validPathState(operation.DesiredHash) {
		return fmt.Errorf("invalid state hash for %s", relPath)
	}
	if operation.Delete {
		if operation.DesiredHash != missingState || len(operation.Content) != 0 {
			return fmt.Errorf("invalid delete operation for %s", relPath)
		}
	} else {
		if len(operation.Content) > maxSyncedFileBytes {
			return fmt.Errorf("file exceeds size limit")
		}
		if fileHash(operation.Content) != operation.DesiredHash {
			return fmt.Errorf("content hash mismatch for %s", relPath)
		}
	}

	parentConflicts, err := c.prepareIncomingParents(relPath, operation.ID)
	if err != nil {
		return err
	}
	for _, conflictRel := range parentConflicts {
		c.notifyWarning(fmt.Sprintf("Conflict: kept local copy at %s", conflictRel))
	}
	destPath, err := secureIncomingDestination(c.baseDir, relPath)
	if err != nil {
		return fmt.Errorf("unsafe path %s: %w", relPath, err)
	}
	currentState, err := pathState(destPath)
	if err != nil {
		return fmt.Errorf("inspect %s: %w", relPath, err)
	}
	committedState := c.committedPathState(relPath)
	ownOperation, newerPending := c.ackPending(relPath, operation.ID)

	if ownOperation && newerPending {
		c.lastHash.Store(relPath, operation.DesiredHash)
		return nil
	}
	if ownOperation && currentState != committedState && currentState != operation.DesiredHash {
		// The user changed the path again before this echo arrived. Commit the
		// ordered echo, keep the newer local bytes, and send them as the next op.
		c.lastHash.Store(relPath, operation.DesiredHash)
		c.scheduleCurrentPath(relPath, destPath)
		return nil
	}
	if currentState == operation.DesiredHash {
		c.storeAppliedState(relPath, operation.DesiredHash)
		return nil
	}

	conflicts, err := c.installIncomingOperation(destPath, relPath, operation, bootstrap)
	if err != nil {
		return err
	}
	for _, conflictRel := range conflicts {
		c.notifyWarning(fmt.Sprintf("Conflict: kept local copy at %s", conflictRel))
	}

	if operation.Delete {
		c.dropPathHashes(relPath)
		c.lastHash.Store(relPath, missingState)
		c.notifyFileReceived(relPath, true)
		return nil
	}

	now := time.Now()
	_ = os.Chtimes(destPath, now, now)
	c.storeAppliedState(relPath, operation.DesiredHash)
	c.notifyFileReceived(relPath, false)
	return nil
}

func (c *Client) applyBootstrapManifest(manifest protocol.BootstrapManifest) error {
	allowed := make(map[string]struct{}, len(manifest.Paths))
	for _, rawPath := range manifest.Paths {
		relPath, err := normalizeIncomingPath(rawPath)
		if err != nil || relPath != rawPath || c.shouldIgnoreInboundRel(relPath) {
			return fmt.Errorf("invalid path in bootstrap manifest")
		}
		allowed[relPath] = struct{}{}
	}

	singleFile := ""
	if manifest.SingleFile != "" {
		var err error
		singleFile, err = normalizeIncomingPath(manifest.SingleFile)
		if err != nil || singleFile != manifest.SingleFile || c.shouldIgnoreInboundRel(singleFile) {
			return fmt.Errorf("invalid single-file scope")
		}
		for relPath := range allowed {
			if relPath != singleFile {
				return fmt.Errorf("bootstrap path is outside file scope")
			}
		}
		if len(manifest.Directories) != 0 {
			return fmt.Errorf("single-file manifest contains directories")
		}
	}
	c.setSingleFileScope(singleFile)

	if singleFile != "" {
		if _, exists := allowed[singleFile]; !exists {
			destination, err := secureIncomingDestination(c.baseDir, singleFile)
			if err != nil {
				return fmt.Errorf("unsafe single-file path: %w", err)
			}
			if _, err := os.Lstat(destination); err == nil {
				conflictRel, err := c.preserveConflict(destination, singleFile, "bootstrap-manifest")
				if err != nil {
					return err
				}
				c.notifyWarning(fmt.Sprintf("Conflict: kept local copy at %s", conflictRel))
			} else if !errors.Is(err, os.ErrNotExist) {
				return err
			}
			c.lastHash.Store(singleFile, missingState)
		}
		return nil
	}

	absent := make([]string, 0)
	err := filepath.WalkDir(c.baseDir, func(currentPath string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if currentPath == c.baseDir {
			return nil
		}
		relPath, err := c.relativeProtocolPath(currentPath)
		if err != nil {
			return err
		}
		if c.shouldIgnoreOutboundRel(relPath, d.IsDir()) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if _, exists := allowed[relPath]; exists {
			return nil
		}
		absent = append(absent, relPath)
		if d.IsDir() {
			return filepath.SkipDir
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("inspect bootstrap destination: %w", err)
	}
	for _, relPath := range absent {
		destination, err := secureIncomingDestination(c.baseDir, relPath)
		if err != nil {
			return err
		}
		conflictRel, err := c.preserveConflict(destination, relPath, "bootstrap-manifest")
		if err != nil {
			return err
		}
		c.dropPathHashes(relPath)
		c.notifyWarning(fmt.Sprintf("Conflict: kept local copy at %s", conflictRel))
	}

	directories := append([]string(nil), manifest.Directories...)
	sort.Slice(directories, func(i, j int) bool {
		return strings.Count(directories[i], "/") < strings.Count(directories[j], "/")
	})
	for _, rawPath := range directories {
		relPath, err := normalizeIncomingPath(rawPath)
		if err != nil || relPath != rawPath {
			return fmt.Errorf("invalid directory in bootstrap manifest")
		}
		if _, exists := allowed[relPath]; !exists {
			return fmt.Errorf("bootstrap directory is not in path set")
		}
		parentConflicts, err := c.prepareIncomingParents(path.Join(relPath, ".placeholder"), "bootstrap-manifest")
		if err != nil {
			return err
		}
		for _, conflictRel := range parentConflicts {
			c.notifyWarning(fmt.Sprintf("Conflict: kept local copy at %s", conflictRel))
		}
		destination := filepath.Join(c.baseDir, filepath.FromSlash(relPath))
		state, err := pathState(destination)
		if err != nil {
			return err
		}
		if state != missingState && state != directoryState {
			conflictRel, err := c.preserveConflict(destination, relPath, "bootstrap-manifest")
			if err != nil {
				return err
			}
			c.notifyWarning(fmt.Sprintf("Conflict: kept local copy at %s", conflictRel))
		}
		if err := os.MkdirAll(destination, 0o755); err != nil {
			return err
		}
		c.lastHash.Store(relPath, directoryState)
	}
	return nil
}

func (c *Client) prepareIncomingParents(relPath, operationID string) ([]string, error) {
	parts := strings.Split(filepath.FromSlash(relPath), string(filepath.Separator))
	current := filepath.Clean(c.baseDir)
	conflicts := make([]string, 0)
	for index, part := range parts[:len(parts)-1] {
		current = filepath.Join(current, part)
		parentRel := filepath.ToSlash(filepath.Join(parts[:index+1]...))
		for attempt := 0; attempt < 4; attempt++ {
			info, err := os.Lstat(current)
			if errors.Is(err, os.ErrNotExist) {
				if err := os.Mkdir(current, 0o755); err == nil || errors.Is(err, os.ErrExist) {
					continue
				} else {
					return nil, err
				}
			}
			if err != nil {
				return nil, err
			}
			if info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
				break
			}
			conflictRel, err := c.preserveConflict(current, parentRel, operationID)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return nil, err
			}
			conflicts = append(conflicts, conflictRel)
		}
		info, err := os.Lstat(current)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("could not create safe parent %s", parentRel)
		}
	}
	return conflicts, nil
}

func (c *Client) installIncomingOperation(destPath, relPath string, operation protocol.SyncOperation, bootstrap bool) ([]string, error) {
	conflicts := make([]string, 0)
	temporaryPath := ""
	if !operation.Delete {
		permission := os.FileMode(0o644)
		if info, err := os.Lstat(destPath); err == nil && info.Mode().IsRegular() {
			permission = info.Mode().Perm()
		}
		temporary, err := os.CreateTemp(filepath.Dir(destPath), ".shadow-incoming-*")
		if err != nil {
			return nil, err
		}
		temporaryPath = temporary.Name()
		defer os.Remove(temporaryPath)
		if err := temporary.Chmod(permission); err != nil {
			_ = temporary.Close()
			return nil, err
		}
		if _, err := temporary.Write(operation.Content); err != nil {
			_ = temporary.Close()
			return nil, err
		}
		if err := temporary.Close(); err != nil {
			return nil, err
		}
	}

	for attempt := 0; attempt < 32; attempt++ {
		if operation.Delete {
			if _, err := os.Lstat(destPath); errors.Is(err, os.ErrNotExist) {
				return conflicts, nil
			} else if err != nil {
				return nil, err
			}
		} else {
			if err := os.Link(temporaryPath, destPath); err == nil {
				return conflicts, nil
			} else if !errors.Is(err, os.ErrExist) {
				return nil, fmt.Errorf("install incoming file without replacement: %w", err)
			}
		}

		conflictRel, err := c.preserveConflict(destPath, relPath, operation.ID)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		conflictPath := filepath.Join(c.baseDir, filepath.FromSlash(conflictRel))
		movedState, err := pathState(conflictPath)
		if err != nil {
			return nil, err
		}
		keepConflict := bootstrap || movedState == directoryState || movedState == otherState || movedState != operation.BaseState || attempt > 0
		if movedState == operation.DesiredHash {
			keepConflict = false
		}
		if keepConflict {
			conflicts = append(conflicts, conflictRel)
		} else if err := os.RemoveAll(conflictPath); err != nil {
			return nil, err
		}
	}
	return nil, fmt.Errorf("path %s changed repeatedly while applying an update", relPath)
}

func validPathState(state string) bool {
	if state == missingState || state == directoryState || state == otherState {
		return true
	}
	if len(state) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(state)
	return err == nil
}

func pathState(filePath string) (string, error) {
	info, err := os.Lstat(filePath)
	if errors.Is(err, os.ErrNotExist) {
		return missingState, nil
	}
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return directoryState, nil
	}
	if !info.Mode().IsRegular() || info.Size() > maxSyncedFileBytes {
		return otherState, nil
	}
	content, err := os.ReadFile(filePath)
	if err != nil {
		return "", err
	}
	return fileHash(content), nil
}

func (c *Client) nextOperationID() string {
	return fmt.Sprintf("%s-%d", c.clientID, c.nextOperation.Add(1))
}

func (c *Client) committedPathState(relPath string) string {
	if state, ok := c.lastHash.Load(relPath); ok {
		return state.(string)
	}
	return missingState
}

func (c *Client) latestPathState(relPath string) string {
	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()
	if pending := c.pending[relPath]; len(pending) > 0 {
		return pending[len(pending)-1].desiredState
	}
	return c.committedPathState(relPath)
}

func (c *Client) addPending(relPath string, operation pendingOperation) {
	c.pendingMu.Lock()
	c.pending[relPath] = append(c.pending[relPath], operation)
	c.pendingMu.Unlock()
}

func (c *Client) removePending(relPath, operationID string) {
	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()
	pending := c.pending[relPath]
	for i := range pending {
		if pending[i].id == operationID {
			pending = append(pending[:i], pending[i+1:]...)
			break
		}
	}
	if len(pending) == 0 {
		delete(c.pending, relPath)
	} else {
		c.pending[relPath] = pending
	}
}

func (c *Client) ackPending(relPath, operationID string) (bool, bool) {
	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()
	pending := c.pending[relPath]
	for i := range pending {
		if pending[i].id != operationID {
			continue
		}
		newer := i < len(pending)-1
		pending = append(pending[:i], pending[i+1:]...)
		if len(pending) == 0 {
			delete(c.pending, relPath)
		} else {
			c.pending[relPath] = pending
		}
		return true, newer
	}
	return false, false
}

func (c *Client) storeAppliedState(relPath, state string) {
	c.lastHash.Store(relPath, state)
	parent := path.Dir(relPath)
	for parent != "." && parent != "/" {
		c.lastHash.Store(parent, directoryState)
		parent = path.Dir(parent)
	}
}

func (c *Client) scheduleCurrentPath(relPath, destPath string) {
	c.scheduleFileTimer(relPath, func() {
		state, err := pathState(destPath)
		if err != nil {
			return
		}
		switch state {
		case missingState:
			c.sendDelete(relPath, true)
		case directoryState:
			c.scheduleRenameRescan()
		default:
			c.SendFile(destPath)
		}
	})
}

func (c *Client) preserveConflict(destPath, relPath, operationID string) (string, error) {
	conflictRoot := filepath.Join(c.baseDir, conflictDirectory)
	if info, err := os.Lstat(conflictRoot); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("conflict path is not a safe directory")
		}
	} else if errors.Is(err, os.ErrNotExist) {
		if err := os.Mkdir(conflictRoot, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return "", err
		}
	} else {
		return "", err
	}

	safeID := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			return r
		}
		return '-'
	}, operationID)
	conflictRel := path.Join(conflictDirectory, relPath+"."+safeID)
	for suffix := 0; ; suffix++ {
		candidateRel := conflictRel
		if suffix > 0 {
			candidateRel = fmt.Sprintf("%s-%d", conflictRel, suffix)
		}
		candidate, err := secureIncomingDestination(c.baseDir, candidateRel)
		if err != nil {
			return "", err
		}
		if _, err := os.Lstat(candidate); err == nil {
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		if err := os.MkdirAll(filepath.Dir(candidate), 0o700); err != nil {
			return "", err
		}
		if err := os.Rename(destPath, candidate); err != nil {
			return "", err
		}
		return filepath.ToSlash(candidateRel), nil
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

func (c *Client) notifyDisconnected() {
	if c.onEvent != nil {
		c.onEvent("disconnected", "", "Disconnected")
		return
	}
	fmt.Println("Disconnected")
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
	defer c.watcherReadyOnce.Do(func() { close(c.watcherReadyCh) })
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

		if c.singleFileScope() != "" && currentPath != c.baseDir {
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
					relPath, relErr := c.relativeProtocolPath(event.Name)
					if relErr == nil && c.shouldIgnoreOutboundRel(relPath, true) {
						continue
					}
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

func (c *Client) scheduleRenameRescan() {
	c.renameRescanMu.Lock()
	defer c.renameRescanMu.Unlock()
	if c.renameRescanTimer != nil {
		c.renameRescanTimer.Stop()
	}
	var timer *time.Timer
	timer = time.AfterFunc(renameRescanDelay, func() {
		c.renameRescanMu.Lock()
		if c.renameRescanTimer != timer {
			c.renameRescanMu.Unlock()
			return
		}
		c.renameRescanTimer = nil
		c.renameRescanMu.Unlock()
		if !c.stopping.Load() {
			c.rescan()
		}
	})
	c.renameRescanTimer = timer
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
	if filepath.Base(filePath) == ".gitignore" {
		c.outboundIgnore.Invalidate()
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
	if wasRename {
		c.scheduleRenameRescan()
	}

	c.scheduleFileTimer(relPath, func() {
		// Rename often emits delete before create; avoid false delete if file reappears.
		if _, statErr := os.Stat(filePath); statErr == nil {
			c.SendFile(filePath)
			return
		}
		c.sendDelete(relPath, true)
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
	if relSlash == "." || relSlash == ".." || len(relSlash) > maxProtocolPathBytes || strings.Contains(relSlash, "\\") || strings.HasPrefix(relSlash, "../") || strings.HasPrefix(relSlash, "/") {
		return "", fmt.Errorf("path %q is outside base directory", filePath)
	}

	if singleFileRel := c.singleFileScope(); singleFileRel != "" && relSlash != singleFileRel {
		return "", fmt.Errorf("path %q is outside file scope", filePath)
	}

	return relSlash, nil
}

func (c *Client) singleFileScope() string {
	c.singleFileMu.RLock()
	defer c.singleFileMu.RUnlock()
	return c.singleFileRel
}

func (c *Client) setSingleFileScope(relPath string) {
	c.singleFileMu.Lock()
	c.singleFileRel = relPath
	c.singleFileMu.Unlock()
}

func normalizeIncomingPath(rawPath string) (string, error) {
	if rawPath == "" || len(rawPath) > maxProtocolPathBytes || strings.ContainsRune(rawPath, '\x00') {
		return "", fmt.Errorf("unsafe path")
	}
	slashPath := strings.ReplaceAll(rawPath, "\\", "/")
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
