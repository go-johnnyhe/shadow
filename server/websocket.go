package server

import (
	"crypto/subtle"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-johnnyhe/shadow/internal/protocol"
	"github.com/go-johnnyhe/shadow/internal/wsutil"
	"github.com/gorilla/websocket"
)

const (
	maxRelayMessageBytes = 20 * 1024 * 1024
	maxQueuedMessages    = 4096
	maxQueuedBytes       = 64 * 1024 * 1024
	maxSessionPeers      = 8
	maxSyncingPeers      = 2
	syncTimeout          = 2 * time.Minute
)

type SessionConfig struct {
	ReadOnlyJoiners bool
	HostToken       string
	JoinToken       string
}

type peerRole uint8

const (
	roleHost peerRole = iota + 1
	roleJoiner
)

type clientPeer interface {
	Write(msgType int, msg []byte) error
}

type outboundMessage struct {
	msgType    int
	data       []byte
	afterWrite func()
}

type relayPeer struct {
	conn clientPeer
	role peerRole
	id   string

	queueMu    sync.Mutex
	queue      []outboundMessage
	queueBytes int
	closed     bool
	wake       chan struct{}
	done       chan struct{}
	stopOnce   sync.Once

	syncing      bool
	pending      []outboundMessage
	pendingBytes int
	syncTimer    *time.Timer
}

func newRelayPeer(conn clientPeer, role peerRole) *relayPeer {
	return &relayPeer{
		conn: conn,
		role: role,
		wake: make(chan struct{}, 1),
		done: make(chan struct{}),
	}
}

func (p *relayPeer) enqueue(message outboundMessage) bool {
	p.queueMu.Lock()
	if p.closed || len(p.queue) >= maxQueuedMessages || p.queueBytes+len(message.data) > maxQueuedBytes {
		p.queueMu.Unlock()
		return false
	}
	p.queue = append(p.queue, message)
	p.queueBytes += len(message.data)
	p.queueMu.Unlock()

	select {
	case p.wake <- struct{}{}:
	default:
	}
	return true
}

func (p *relayPeer) nextMessage() (outboundMessage, bool) {
	for {
		p.queueMu.Lock()
		if len(p.queue) > 0 {
			message := p.queue[0]
			p.queue[0] = outboundMessage{}
			p.queue = p.queue[1:]
			p.queueBytes -= len(message.data)
			p.queueMu.Unlock()
			return message, true
		}
		closed := p.closed
		p.queueMu.Unlock()
		if closed {
			return outboundMessage{}, false
		}

		select {
		case <-p.wake:
		case <-p.done:
			return outboundMessage{}, false
		}
	}
}

func (p *relayPeer) writeLoop(session *sessionRelay) {
	for {
		message, ok := p.nextMessage()
		if !ok {
			return
		}
		if err := p.conn.Write(message.msgType, message.data); err != nil {
			session.unregister(p)
			return
		}
		if message.afterWrite != nil {
			message.afterWrite()
		}
	}
}

func (p *relayPeer) stop() {
	p.stopOnce.Do(func() {
		p.queueMu.Lock()
		p.closed = true
		p.queue = nil
		p.queueBytes = 0
		p.queueMu.Unlock()
		close(p.done)
		if p.syncTimer != nil {
			p.syncTimer.Stop()
		}
		if closer, ok := p.conn.(interface{ Close() error }); ok {
			_ = closer.Close()
		}
	})
}

type sessionRelay struct {
	mu         sync.Mutex
	config     SessionConfig
	peers      map[*relayPeer]struct{}
	host       *relayPeer
	nextPeerID uint64
	sequence   uint64
}

func newSessionRelay() *sessionRelay {
	return &sessionRelay{peers: make(map[*relayPeer]struct{})}
}

type Relay struct {
	session *sessionRelay
}

func NewRelay(config SessionConfig) *Relay {
	session := newSessionRelay()
	session.config = config
	return &Relay{session: session}
}

func (r *Relay) ServeHTTP(w http.ResponseWriter, request *http.Request) {
	r.session.serveHTTP(w, request)
}

func (s *sessionRelay) roleForRequest(r *http.Request) (peerRole, bool) {
	authorization := r.Header.Get("Authorization")
	const bearer = "Bearer "
	if !strings.HasPrefix(authorization, bearer) {
		return 0, false
	}
	token := strings.TrimSpace(strings.TrimPrefix(authorization, bearer))

	s.mu.Lock()
	config := s.config
	s.mu.Unlock()
	if config.HostToken == "" || config.JoinToken == "" {
		return 0, false
	}
	if secureTokenEqual(config.HostToken, config.JoinToken) {
		return 0, false
	}
	if secureTokenEqual(token, config.HostToken) {
		return roleHost, true
	}
	if secureTokenEqual(token, config.JoinToken) {
		return roleJoiner, true
	}
	return 0, false
}

func secureTokenEqual(got, expected string) bool {
	if len(got) != len(expected) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(expected)) == 1
}

func (s *sessionRelay) register(peer *relayPeer) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.peers) >= maxSessionPeers {
		return false
	}
	if peer.role == roleHost {
		if s.host != nil {
			return false
		}
		s.host = peer
	} else if s.host == nil {
		return false
	} else if s.syncingPeerCountLocked() >= maxSyncingPeers {
		return false
	}

	s.nextPeerID++
	peer.id = fmt.Sprintf("%d", s.nextPeerID)
	peer.syncing = peer.role == roleJoiner
	s.peers[peer] = struct{}{}

	if !peer.enqueue(outboundMessage{
		msgType: websocket.TextMessage,
		data:    protocol.EncodeControlReadOnlyJoiners(s.config.ReadOnlyJoiners),
	}) {
		s.removePeerLocked(peer)
		return false
	}

	if peer.syncing {
		if !peer.enqueue(outboundMessage{
			msgType: websocket.TextMessage,
			data:    protocol.EncodeControlSyncBaseline(s.sequence),
		}) {
			s.removePeerLocked(peer)
			return false
		}
		if !s.host.enqueue(outboundMessage{
			msgType:    websocket.TextMessage,
			data:       protocol.EncodeControlSyncRequest(peer.id),
			afterWrite: func() { s.startSyncTimer(peer) },
		}) {
			s.removePeerLocked(s.host)
			return false
		}
	}

	s.broadcastPeerCountLocked()
	return true
}

func (s *sessionRelay) startSyncTimer(peer *relayPeer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.peers[peer]; !ok || !peer.syncing || peer.syncTimer != nil {
		return
	}
	peer.syncTimer = time.AfterFunc(syncTimeout, func() {
		s.timeoutSync(peer)
	})
}

func (s *sessionRelay) timeoutSync(peer *relayPeer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.peers[peer]; !ok || !peer.syncing {
		return
	}
	s.removePeerLocked(peer)
	if len(s.peers) > 0 {
		s.broadcastPeerCountLocked()
	}
}

func (s *sessionRelay) unregister(peer *relayPeer) {
	s.mu.Lock()
	if _, ok := s.peers[peer]; !ok {
		s.mu.Unlock()
		return
	}
	s.removePeerLocked(peer)
	if len(s.peers) > 0 {
		s.broadcastPeerCountLocked()
	}
	s.mu.Unlock()
}

func (s *sessionRelay) removePeerLocked(peer *relayPeer) {
	if peer == nil {
		return
	}
	if _, ok := s.peers[peer]; !ok {
		return
	}
	delete(s.peers, peer)
	peer.stop()

	if peer == s.host {
		s.host = nil
		for other := range s.peers {
			delete(s.peers, other)
			other.stop()
		}
	}
}

func (s *sessionRelay) broadcastPeerCountLocked() {
	message := outboundMessage{
		msgType: websocket.TextMessage,
		data:    protocol.EncodeControlPeerCount(len(s.peers)),
	}
	failed := make([]*relayPeer, 0)
	for peer := range s.peers {
		if !peer.enqueue(message) {
			failed = append(failed, peer)
		}
	}
	for _, peer := range failed {
		s.removePeerLocked(peer)
	}
}

func (s *sessionRelay) acceptNormal(source *relayPeer, encryptedPayload string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.peers[source]; !ok || source.syncing {
		return false
	}
	if source.role == roleJoiner && s.config.ReadOnlyJoiners {
		return false
	}

	s.sequence++
	message := outboundMessage{
		msgType: websocket.TextMessage,
		data:    protocol.EncodeOrderedEncrypted(s.sequence, encryptedPayload),
	}
	failed := make([]*relayPeer, 0)
	for peer := range s.peers {
		if peer.syncing {
			if len(peer.pending) >= maxQueuedMessages || peer.pendingBytes+len(message.data) > maxQueuedBytes {
				failed = append(failed, peer)
				continue
			}
			peer.pending = append(peer.pending, message)
			peer.pendingBytes += len(message.data)
			continue
		}
		if !peer.enqueue(message) {
			failed = append(failed, peer)
		}
	}
	for _, peer := range failed {
		s.removePeerLocked(peer)
	}
	return true
}

func (s *sessionRelay) acceptBootstrap(source *relayPeer, targetID, encryptedPayload string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if source != s.host {
		return false
	}
	target := s.syncingPeerLocked(targetID)
	if target == nil {
		return true
	}
	if !target.enqueue(outboundMessage{
		msgType: websocket.TextMessage,
		data:    protocol.EncodeBootstrapEncrypted(encryptedPayload),
	}) {
		s.removePeerLocked(target)
		return true
	}
	return true
}

func (s *sessionRelay) completeSync(source *relayPeer, targetID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if source != s.host {
		return false
	}
	target := s.syncingPeerLocked(targetID)
	if target == nil {
		return true
	}

	for _, message := range target.pending {
		if !target.enqueue(message) {
			s.removePeerLocked(target)
			return true
		}
	}
	if !target.enqueue(outboundMessage{
		msgType: websocket.TextMessage,
		data:    protocol.EncodeControlSyncComplete(),
	}) {
		s.removePeerLocked(target)
		return true
	}
	target.pending = nil
	target.pendingBytes = 0
	target.syncing = false
	if target.syncTimer != nil {
		target.syncTimer.Stop()
		target.syncTimer = nil
	}
	return true
}

func (s *sessionRelay) syncingPeerLocked(peerID string) *relayPeer {
	for peer := range s.peers {
		if peer.id == peerID && peer.syncing {
			return peer
		}
	}
	return nil
}

func (s *sessionRelay) syncingPeerCountLocked() int {
	count := 0
	for peer := range s.peers {
		if peer.syncing {
			count++
		}
	}
	return count
}

func (s *sessionRelay) enqueuePing(peer *relayPeer) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.peers[peer]; !ok {
		return false
	}
	if !peer.enqueue(outboundMessage{msgType: websocket.PingMessage}) {
		s.removePeerLocked(peer)
		return false
	}
	return true
}

var upgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	Subprotocols:    []string{protocol.WebSocketSubprotocol},
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

func (s *sessionRelay) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if !contains(websocket.Subprotocols(r), protocol.WebSocketSubprotocol) {
		http.Error(w, "Shadow protocol v2 is required", http.StatusUpgradeRequired)
		return
	}
	role, authorized := s.roleForRequest(r)
	if !authorized {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	conn.SetReadLimit(maxRelayMessageBytes)
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	})

	peer := newRelayPeer(wsutil.NewPeer(conn), role)
	if !s.register(peer) {
		peer.stop()
		return
	}
	go peer.writeLoop(s)

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	go func() {
		for {
			select {
			case <-ticker.C:
				if !s.enqueuePing(peer) {
					return
				}
			case <-peer.done:
				return
			}
		}
	}()

	defer s.unregister(peer)
	for {
		msgType, message, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err,
				websocket.CloseNormalClosure,
				websocket.CloseGoingAway,
				websocket.CloseAbnormalClosure,
				websocket.CloseNoStatusReceived) {
				log.Printf("websocket read error: %v", err)
			}
			return
		}
		if msgType != websocket.TextMessage || !handleClientMessage(s, peer, message) {
			return
		}
	}
}

func handleClientMessage(session *sessionRelay, peer *relayPeer, message []byte) bool {
	prefix := protocol.EncryptedChannel + "|"
	if strings.HasPrefix(string(message), prefix) && len(message) > len(prefix) {
		return session.acceptNormal(peer, string(message[len(prefix):]))
	}
	if targetID, payload, ok := protocol.ParseTargetedEncrypted(message); ok {
		return session.acceptBootstrap(peer, targetID, payload)
	}
	if targetID, ok := protocol.ParseSyncDone(message); ok {
		return session.completeSync(peer, targetID)
	}
	return false
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
