package ws

import (
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"traffic-center/backend/metrics"
	"traffic-center/backend/store"
)

type BufferProvider interface {
	GetBuffer(projectID string) (*metrics.Buffer, bool)
}

type Handler struct {
	Store   *store.Store
	Buffers BufferProvider
	Hub     *Hub
}

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 4096,
	CheckOrigin: func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		return origin == "" ||
			strings.HasPrefix(origin, "http://localhost:") ||
			strings.HasPrefix(origin, "http://127.0.0.1:")
	},
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	const prefix = "/ws/"
	if !strings.HasPrefix(r.URL.Path, prefix) {
		http.Error(w, "route not found", http.StatusNotFound)
		return
	}

	projectID := strings.TrimPrefix(r.URL.Path, prefix)
	if projectID == "" || strings.Contains(projectID, "/") {
		http.Error(w, "invalid project ID", http.StatusBadRequest)
		return
	}

	if h.Store == nil || h.Buffers == nil {
		http.Error(w, "WebSocket service unavailable", http.StatusInternalServerError)
		return
	}

	if _, err := h.Store.GetProject(projectID); err != nil {
		http.Error(w, "project not found", http.StatusNotFound)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("WebSocket upgrade failed: %v", err)
		return
	}
	defer conn.Close()

	// Read messages so disconnects and control frames are detected.
	conn.SetReadLimit(512)
	_ = conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	})

	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()

	// Register with Hub if present
	var client *ClientConn
	if h.Hub != nil {
		client = h.Hub.Register(projectID)
		defer h.Hub.Unregister(projectID, client)
	}

	// Send the current snapshot event immediately upon connection.
	if !h.sendInitialSnapshot(conn, projectID) {
		return
	}

	pingTicker := time.NewTicker(30 * time.Second)
	defer pingTicker.Stop()

	for {
		if client != nil {
			select {
			case <-done:
				return

			case msg, ok := <-client.send:
				if !ok {
					return
				}
				_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
				if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
					return
				}

			case <-pingTicker.C:
				if err := conn.WriteControl(
					websocket.PingMessage,
					nil,
					time.Now().Add(5*time.Second),
				); err != nil {
					return
				}
			}
		} else {
			// Fallback if Hub is nil
			select {
			case <-done:
				return
			case <-pingTicker.C:
				if err := conn.WriteControl(
					websocket.PingMessage,
					nil,
					time.Now().Add(5*time.Second),
				); err != nil {
					return
				}
			}
		}
	}
}

func (h *Handler) sendInitialSnapshot(conn *websocket.Conn, projectID string) bool {
	buffer, ok := h.Buffers.GetBuffer(projectID)

	var snapshot metrics.Snapshot
	if ok && buffer != nil {
		snapshot = metrics.Compute(buffer.Window(time.Now()), 60)
	} else {
		snapshot = metrics.Compute(nil, 60)
	}

	event := EventEnvelope{
		EventType: EventMetricSnapshot,
		ProjectID: projectID,
		Timestamp: time.Now().UTC(),
		Data:      snapshot,
	}

	_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if err := conn.WriteJSON(event); err != nil {
		return false
	}

	return true
}
