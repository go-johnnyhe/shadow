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

var clients = make(map[*wsutil.Peer]bool)
var clientsMutex = &sync.Mutex{}
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

	log.Println("Connected to websocket!")

	clientsMutex.Lock()
	clients[p] = true
	clientsMutex.Unlock()

	defer func() {
		conn.Close()
		clientsMutex.Lock()
		delete(clients, p)
		clientsMutex.Unlock()
		log.Printf("Client disconnected. Total clients now: %d", len(clients))
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
			clientsMutex.Lock()
			for client := range clients {
				if client != p {
					err := client.Write(msgType, msg)
					if err != nil {
						fmt.Println("Error writing message to other clients: ", err)
					}
				}
			}
			clientsMutex.Unlock()
		}
	}
}
