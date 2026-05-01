package api

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"

	"yt-automation-studio/models"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true // Allow all origins (local only)
	},
}

// wsClients tracks active WebSocket connections per job ID
var (
	wsClients = make(map[string]map[*websocket.Conn]bool)
	wsMu      sync.RWMutex
)

// handleWebSocket upgrades HTTP to WebSocket for real-time progress
func handleWebSocket(w http.ResponseWriter, r *http.Request) {
	jobID := r.PathValue("id")
	if jobID == "" {
		http.Error(w, "Missing job ID", http.StatusBadRequest)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("⚠️  WebSocket upgrade failed: %v", err)
		return
	}

	// Register client
	wsMu.Lock()
	if wsClients[jobID] == nil {
		wsClients[jobID] = make(map[*websocket.Conn]bool)
	}
	wsClients[jobID][conn] = true
	wsMu.Unlock()

	log.Printf("🔌 WebSocket connected for job %s", jobID[:8])

	// Keep connection alive — read messages (ping/pong handled by gorilla)
	defer func() {
		wsMu.Lock()
		delete(wsClients[jobID], conn)
		if len(wsClients[jobID]) == 0 {
			delete(wsClients, jobID)
		}
		wsMu.Unlock()
		conn.Close()
		log.Printf("🔌 WebSocket disconnected for job %s", jobID[:8])
	}()

	for {
		_, _, err := conn.ReadMessage()
		if err != nil {
			break
		}
	}
}

// BroadcastProgress sends a progress event to all WebSocket clients watching a job
func BroadcastProgress(jobID string, event models.ProgressEvent) {
	wsMu.RLock()
	clients, exists := wsClients[jobID]
	wsMu.RUnlock()

	if !exists || len(clients) == 0 {
		return
	}

	data, err := json.Marshal(event)
	if err != nil {
		return
	}

	wsMu.RLock()
	defer wsMu.RUnlock()

	for conn := range clients {
		if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
			log.Printf("⚠️  WebSocket write error: %v", err)
			conn.Close()
			delete(clients, conn)
		}
	}
}
