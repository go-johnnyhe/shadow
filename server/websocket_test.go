package server

import (
	"errors"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

type mockPeer struct {
	writeFn func(msgType int, msg []byte) error
}

func (m *mockPeer) Write(msgType int, msg []byte) error {
	if m.writeFn != nil {
		return m.writeFn(msgType, msg)
	}
	return nil
}

func resetClientsForTest(t *testing.T) {
	t.Helper()

	oldClients := clients
	clients = make(map[clientPeer]struct{})
	t.Cleanup(func() {
		clients = oldClients
	})
}

func TestBroadcastPeerCountDoesNotHoldMutexDuringWrites(t *testing.T) {
	resetClientsForTest(t)

	enterWrite := make(chan struct{})
	releaseWrite := make(chan struct{})
	blocking := &mockPeer{
		writeFn: func(msgType int, msg []byte) error {
			close(enterWrite)
			<-releaseWrite
			return nil
		},
	}

	clients[blocking] = struct{}{}

	done := make(chan struct{})
	go func() {
		broadcastPeerCount(nil, 1)
		close(done)
	}()

	select {
	case <-enterWrite:
	case <-time.After(time.Second):
		t.Fatalf("broadcast did not start writing")
	}

	locked := make(chan struct{})
	go func() {
		clientsMutex.Lock()
		clientsMutex.Unlock()
		close(locked)
	}()

	select {
	case <-locked:
	case <-time.After(200 * time.Millisecond):
		t.Fatalf("clients mutex remained locked while write was blocked")
	}

	close(releaseWrite)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatalf("broadcast did not complete")
	}
}

func TestBroadcastTextPrunesFailedPeers(t *testing.T) {
	resetClientsForTest(t)

	okPeer := &mockPeer{}
	failedPeer := &mockPeer{
		writeFn: func(msgType int, msg []byte) error {
			return errors.New("write failed")
		},
	}

	clients[okPeer] = struct{}{}
	clients[failedPeer] = struct{}{}

	broadcastText(nil, websocket.TextMessage, []byte("hello"))

	clientsMutex.Lock()
	_, okStillPresent := clients[okPeer]
	_, failedStillPresent := clients[failedPeer]
	clientsMutex.Unlock()

	if !okStillPresent {
		t.Fatalf("healthy peer should remain registered")
	}
	if failedStillPresent {
		t.Fatalf("failed peer should be pruned")
	}
}

func TestRelayAcceptsOnlyEncryptedClientMessages(t *testing.T) {
	tests := []struct {
		name string
		msg  string
		want bool
	}{
		{name: "encrypted", msg: "__shadow_e2e__|ciphertext", want: true},
		{name: "empty encrypted payload", msg: "__shadow_e2e__|", want: false},
		{name: "spoofed control", msg: "__shadow_control__|read_only_joiners=1", want: false},
		{name: "plaintext file", msg: "file.txt|contents", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isRelayableClientMessage([]byte(tt.msg)); got != tt.want {
				t.Fatalf("isRelayableClientMessage(%q) = %v, want %v", tt.msg, got, tt.want)
			}
		})
	}
}
