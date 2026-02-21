package server

import (
	"fmt"
	"github.com/go-johnnyhe/shadow/internal/protocol"
	"github.com/go-johnnyhe/shadow/internal/wsutil"
	"github.com/gorilla/websocket"
	"log"
	"net/http"
	"sync"
	"time"
)

type clientPeer interface {
	Write(msgType int, msg []byte) error
}

var clients = make(map[clientPeer]struct{})
var clientsMutex sync.Mutex
var sessionOptions = struct {
	mu              sync.RWMutex
	readOnlyJoiners bool
}{}
var upgrader = websocket.Upgrader{
	ReadBufferSize:    4096,
	WriteBufferSize:   4096,
	EnableCompression: true,
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

func SetReadOnlyJoiners(enabled bool) {
	sessionOptions.mu.Lock()
	sessionOptions.readOnlyJoiners = enabled
	sessionOptions.mu.Unlock()
}

func getReadOnlyJoiners() bool {
	sessionOptions.mu.RLock()
	defer sessionOptions.mu.RUnlock()
	return sessionOptions.readOnlyJoiners
}

func snapshotClients(exclude clientPeer) []clientPeer {
	clientsMutex.Lock()
	defer clientsMutex.Unlock()

	targets := make([]clientPeer, 0, len(clients))
	for client := range clients {
		if client != exclude {
			targets = append(targets, client)
		}
	}
	return targets
}

func writeToClients(targets []clientPeer, msgType int, msg []byte) []clientPeer {
	stale := make([]clientPeer, 0)
	for _, client := range targets {
		if err := client.Write(msgType, msg); err != nil {
			stale = append(stale, client)
		}
	}
	return stale
}

func removeClients(stale []clientPeer) int {
	if len(stale) == 0 {
		clientsMutex.Lock()
		count := len(clients)
		clientsMutex.Unlock()
		return count
	}

	clientsMutex.Lock()
	for _, client := range stale {
		delete(clients, client)
	}
	count := len(clients)
	clientsMutex.Unlock()

	for _, client := range stale {
		if closer, ok := client.(interface{ Close() error }); ok {
			_ = closer.Close()
		}
	}
	return count
}

func broadcastPeerCount(exclude clientPeer, count int) {
	msg := protocol.EncodeControlPeerCount(count)
	targets := snapshotClients(exclude)
	stale := writeToClients(targets, websocket.TextMessage, msg)
	removeClients(stale)
}

func broadcastText(exclude clientPeer, msgType int, msg []byte) {
	targets := snapshotClients(exclude)
	stale := writeToClients(targets, msgType, msg)
	if len(stale) == 0 {
		return
	}
	peerCount := removeClients(stale)
	broadcastPeerCount(nil, peerCount)
}

func StartServer(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		fmt.Println("Error upgrading to websocket connection: ", err)
		return
	}

	conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	p := wsutil.NewPeer(conn)

	if err := p.Write(websocket.TextMessage, protocol.EncodeControlReadOnlyJoiners(getReadOnlyJoiners())); err != nil {
		log.Printf("Failed to send session options: %v", err)
		conn.Close()
		return
	}

	go func() {
		for range ticker.C {
			if err := p.Write(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}()

	clientsMutex.Lock()
	clients[p] = struct{}{}
	peerCount := len(clients)
	clientsMutex.Unlock()
	broadcastPeerCount(p, peerCount)

	defer func() {
		conn.Close()
		clientsMutex.Lock()
		delete(clients, p)
		peerCount := len(clients)
		clientsMutex.Unlock()
		broadcastPeerCount(nil, peerCount)
	}()

	for {
		msgType, msg, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				log.Printf("websocket read error: %v", err)
			}
			break
		}
		if msgType == websocket.TextMessage {
			broadcastText(p, msgType, msg)
		}
	}
}
