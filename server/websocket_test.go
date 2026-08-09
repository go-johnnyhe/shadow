package server

import (
	"fmt"
	"net/http/httptest"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/go-johnnyhe/shadow/internal/protocol"
)

type mockPeer struct {
	mu       sync.Mutex
	messages [][]byte
}

func (m *mockPeer) Write(_ int, msg []byte) error {
	m.mu.Lock()
	m.messages = append(m.messages, append([]byte(nil), msg...))
	m.mu.Unlock()
	return nil
}

func (m *mockPeer) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.messages)
}

func testSession(readOnly bool) *sessionRelay {
	session := newSessionRelay()
	session.config = SessionConfig{ReadOnlyJoiners: readOnly, HostToken: "host-token", JoinToken: "join-token"}
	return session
}

func clearQueue(peer *relayPeer) {
	peer.queueMu.Lock()
	peer.queue = nil
	peer.queueBytes = 0
	peer.queueMu.Unlock()
}

func TestReadOnlyJoinerUpdateIsRejectedByRelay(t *testing.T) {
	session := testSession(true)
	host := newRelayPeer(&mockPeer{}, roleHost)
	joiner := newRelayPeer(&mockPeer{}, roleJoiner)
	if !session.register(host) || !session.register(joiner) {
		t.Fatal("failed to register test peers")
	}
	if !session.completeSync(host, joiner.id) {
		t.Fatal("failed to complete joiner sync")
	}
	if session.acceptNormal(joiner, "ciphertext") {
		t.Fatal("read-only joiner update was accepted")
	}
}

func TestRelayEchoesOrderedUpdatesToSender(t *testing.T) {
	session := testSession(false)
	host := newRelayPeer(&mockPeer{}, roleHost)
	if !session.register(host) {
		t.Fatal("failed to register host")
	}
	clearQueue(host)
	if !session.acceptNormal(host, "ciphertext") {
		t.Fatal("host update was rejected")
	}
	host.queueMu.Lock()
	defer host.queueMu.Unlock()
	if len(host.queue) != 1 {
		t.Fatalf("sender queue has %d messages, want 1", len(host.queue))
	}
	sequence, payload, ok := protocol.ParseOrderedEncrypted(host.queue[0].data)
	if !ok || sequence != 1 || payload != "ciphertext" {
		t.Fatalf("unexpected ordered echo: sequence=%d payload=%q ok=%v", sequence, payload, ok)
	}
}

func TestBootstrapBarrierQueuesOrderedUpdates(t *testing.T) {
	session := testSession(false)
	host := newRelayPeer(&mockPeer{}, roleHost)
	joiner := newRelayPeer(&mockPeer{}, roleJoiner)
	if !session.register(host) || !session.register(joiner) {
		t.Fatal("failed to register test peers")
	}
	clearQueue(joiner)
	if !session.acceptNormal(host, "ordered") {
		t.Fatal("ordered update was rejected")
	}
	if !session.acceptBootstrap(host, joiner.id, "snapshot") {
		t.Fatal("bootstrap update was rejected")
	}
	if !session.completeSync(host, joiner.id) {
		t.Fatal("failed to complete sync")
	}

	joiner.queueMu.Lock()
	defer joiner.queueMu.Unlock()
	if len(joiner.queue) != 3 {
		t.Fatalf("joiner queue has %d messages, want 3", len(joiner.queue))
	}
	if payload, ok := protocol.ParseBootstrapEncrypted(joiner.queue[0].data); !ok || payload != "snapshot" {
		t.Fatalf("first message is not the bootstrap: %q", joiner.queue[0].data)
	}
	if _, payload, ok := protocol.ParseOrderedEncrypted(joiner.queue[1].data); !ok || payload != "ordered" {
		t.Fatalf("second message is not the queued update: %q", joiner.queue[1].data)
	}
	parts := string(joiner.queue[2].data)
	if parts != string(protocol.EncodeControlSyncComplete()) {
		t.Fatalf("last message is not sync_complete: %q", parts)
	}
}

func TestStaleBootstrapTargetDoesNotDisconnectHost(t *testing.T) {
	session := testSession(false)
	host := newRelayPeer(&mockPeer{}, roleHost)
	joiner := newRelayPeer(&mockPeer{}, roleJoiner)
	if !session.register(host) || !session.register(joiner) {
		t.Fatal("failed to register test peers")
	}
	session.unregister(joiner)
	if !session.acceptBootstrap(host, joiner.id, "late-snapshot") {
		t.Fatal("stale bootstrap target was treated as a host protocol error")
	}
	if !session.completeSync(host, joiner.id) {
		t.Fatal("stale sync completion was treated as a host protocol error")
	}
	session.mu.Lock()
	_, hostStillRegistered := session.peers[host]
	session.mu.Unlock()
	if !hostStillRegistered {
		t.Fatal("host was disconnected with stale bootstrap target")
	}
}

func TestCompletedSyncIgnoresLateTimeout(t *testing.T) {
	session := testSession(false)
	host := newRelayPeer(&mockPeer{}, roleHost)
	joiner := newRelayPeer(&mockPeer{}, roleJoiner)
	if !session.register(host) || !session.register(joiner) || !session.completeSync(host, joiner.id) {
		t.Fatal("failed to complete test sync")
	}
	session.timeoutSync(joiner)
	session.mu.Lock()
	_, joinerStillRegistered := session.peers[joiner]
	session.mu.Unlock()
	if !joinerStillRegistered {
		t.Fatal("late timeout removed a synchronized joiner")
	}
}

func TestSyncTimeoutStartsAfterHostReceivesRequest(t *testing.T) {
	session := testSession(false)
	host := newRelayPeer(&mockPeer{}, roleHost)
	joiner := newRelayPeer(&mockPeer{}, roleJoiner)
	if !session.register(host) || !session.register(joiner) {
		t.Fatal("failed to register test peers")
	}
	if joiner.syncTimer != nil {
		t.Fatal("sync timeout started before host write")
	}
	go host.writeLoop(session)
	t.Cleanup(func() { session.unregister(host) })

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		session.mu.Lock()
		started := joiner.syncTimer != nil
		session.mu.Unlock()
		if started {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("sync timeout did not start after host write")
}

func TestRelayLimitsConcurrentBootstraps(t *testing.T) {
	session := testSession(false)
	host := newRelayPeer(&mockPeer{}, roleHost)
	if !session.register(host) {
		t.Fatal("failed to register host")
	}
	for i := 0; i < maxSyncingPeers; i++ {
		if !session.register(newRelayPeer(&mockPeer{}, roleJoiner)) {
			t.Fatalf("joiner %d was rejected below the sync limit", i)
		}
	}
	if session.register(newRelayPeer(&mockPeer{}, roleJoiner)) {
		t.Fatal("relay accepted a joiner above the sync limit")
	}
}

func TestRelayRejectsInvalidTokenAndOldProtocol(t *testing.T) {
	relay := NewRelay(SessionConfig{HostToken: "host-token", JoinToken: "join-token"})

	oldRequest := httptest.NewRequest("GET", "http://example.test/ws", nil)
	oldRequest.Header.Set("Authorization", "Bearer host-token")
	oldResponse := httptest.NewRecorder()
	relay.ServeHTTP(oldResponse, oldRequest)
	if oldResponse.Code != 426 {
		t.Fatalf("old protocol status = %d, want 426", oldResponse.Code)
	}

	badTokenRequest := httptest.NewRequest("GET", "http://example.test/ws", nil)
	badTokenRequest.Header.Set("Authorization", "Bearer wrong-token")
	badTokenRequest.Header.Set("Sec-WebSocket-Protocol", protocol.WebSocketSubprotocol)
	badTokenResponse := httptest.NewRecorder()
	relay.ServeHTTP(badTokenResponse, badTokenRequest)
	if badTokenResponse.Code != 401 {
		t.Fatalf("bad token status = %d, want 401", badTokenResponse.Code)
	}
}

func TestRelayRejectsEqualRoleTokens(t *testing.T) {
	relay := NewRelay(SessionConfig{HostToken: "same-token", JoinToken: "same-token"})
	request := httptest.NewRequest("GET", "http://example.test/ws", nil)
	request.Header.Set("Authorization", "Bearer same-token")
	request.Header.Set("Sec-WebSocket-Protocol", protocol.WebSocketSubprotocol)
	response := httptest.NewRecorder()
	relay.ServeHTTP(response, request)
	if response.Code != 401 {
		t.Fatalf("equal role tokens status = %d, want 401", response.Code)
	}
}

func TestSlowPeerDoesNotBlockHealthyPeers(t *testing.T) {
	session := testSession(false)
	hostOutput := &mockPeer{}
	healthyOutput := &mockPeer{}
	host := newRelayPeer(hostOutput, roleHost)
	healthy := newRelayPeer(healthyOutput, roleJoiner)
	slow := newRelayPeer(&mockPeer{}, roleJoiner)
	if !session.register(host) {
		t.Fatal("failed to register host")
	}
	go host.writeLoop(session)
	if !session.register(healthy) || !session.completeSync(host, healthy.id) {
		t.Fatal("failed to register healthy peer")
	}
	go healthy.writeLoop(session)
	if !session.register(slow) || !session.completeSync(host, slow.id) {
		t.Fatal("failed to register slow peer")
	}

	for i := 0; i < maxQueuedMessages+20; i++ {
		if !session.acceptNormal(host, fmt.Sprintf("message-%d", i)) {
			t.Fatalf("host update %d was rejected", i)
		}
		runtime.Gosched()
	}
	deadline := time.Now().Add(time.Second)
	for healthyOutput.count() < maxQueuedMessages && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if healthyOutput.count() < maxQueuedMessages {
		t.Fatalf("healthy peer received only %d messages", healthyOutput.count())
	}
	session.mu.Lock()
	_, slowStillRegistered := session.peers[slow]
	_, healthyStillRegistered := session.peers[healthy]
	session.mu.Unlock()
	if slowStillRegistered {
		t.Fatal("slow peer was not disconnected after queue overflow")
	}
	if !healthyStillRegistered {
		t.Fatal("healthy peer was disconnected")
	}
	session.unregister(host)
}

func TestRelayAcceptsOnlyProtocolMessages(t *testing.T) {
	session := testSession(false)
	host := newRelayPeer(&mockPeer{}, roleHost)
	if !session.register(host) {
		t.Fatal("failed to register host")
	}
	if !handleClientMessage(session, host, protocol.EncodeEncrypted("ciphertext")) {
		t.Fatal("encrypted update was rejected")
	}
	if handleClientMessage(session, host, []byte("file.txt|contents")) {
		t.Fatal("plaintext update was accepted")
	}
	if handleClientMessage(session, host, protocol.EncodeControlReadOnlyJoiners(true)) {
		t.Fatal("spoofed control message was accepted")
	}
}
