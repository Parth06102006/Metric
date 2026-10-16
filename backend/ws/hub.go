package ws

import (
	"encoding/json"
	"sync"
	"time"

	"traffic-center/backend/metrics"
)

// EventType defines the category of websocket message.
type EventType string

const (
	EventMetricSnapshot EventType = "metric_snapshot"
	EventAnomalyAlert   EventType = "anomaly_alert"
)

// AnomalyPayload represents the data inside an anomaly_alert event.
type AnomalyPayload struct {
	Type       string    `json:"type"`
	Severity   string    `json:"severity"`
	Message    string    `json:"message"`
	Value      float64   `json:"value"`
	Threshold  float64   `json:"threshold"`
	Analysis   string    `json:"analysis"`
	DetectedAt time.Time `json:"detected_at"`
}

// EventEnvelope is the standard envelope delivered over WebSocket.
type EventEnvelope struct {
	EventType EventType `json:"event_type"`
	ProjectID string    `json:"project_id"`
	Timestamp time.Time `json:"timestamp"`
	Data      any       `json:"data"`
}

// ClientConn wraps an active websocket subscriber.
type ClientConn struct {
	send chan []byte
}

// Hub maintains active client connections per project and broadcasts events.
type Hub struct {
	mu          sync.RWMutex
	subscribers map[string]map[*ClientConn]struct{}
}

// NewHub creates a new pub/sub hub.
func NewHub() *Hub {
	return &Hub{
		subscribers: make(map[string]map[*ClientConn]struct{}),
	}
}

// Register adds a client connection for a specific project.
func (h *Hub) Register(projectID string) *ClientConn {
	h.mu.Lock()
	defer h.mu.Unlock()

	client := &ClientConn{
		send: make(chan []byte, 64),
	}

	if _, exists := h.subscribers[projectID]; !exists {
		h.subscribers[projectID] = make(map[*ClientConn]struct{})
	}
	h.subscribers[projectID][client] = struct{}{}

	return client
}

// Unregister removes a client connection and closes its send channel.
func (h *Hub) Unregister(projectID string, client *ClientConn) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if clients, exists := h.subscribers[projectID]; exists {
		if _, ok := clients[client]; ok {
			delete(clients, client)
			close(client.send)
		}
		if len(clients) == 0 {
			delete(h.subscribers, projectID)
		}
	}
}

// SubscriberCount returns the active subscriber count for a project.
func (h *Hub) SubscriberCount(projectID string) int {
	h.mu.RLock()
	defer h.mu.RUnlock()

	if clients, exists := h.subscribers[projectID]; exists {
		return len(clients)
	}
	return 0
}

// Broadcast sends raw event bytes to all active clients of a project.
func (h *Hub) Broadcast(projectID string, event EventEnvelope) {
	msgBytes, err := json.Marshal(event)
	if err != nil {
		return
	}

	h.mu.RLock()
	clients := make([]*ClientConn, 0)
	if subs, exists := h.subscribers[projectID]; exists {
		for c := range subs {
			clients = append(clients, c)
		}
	}
	h.mu.RUnlock()

	for _, client := range clients {
		select {
		case client.send <- msgBytes:
		default:
			// Client send channel full: drop message to prevent blocking pipeline
		}
	}
}

// BroadcastSnapshot helper for metric_snapshot event.
func (h *Hub) BroadcastSnapshot(projectID string, snapshot metrics.Snapshot) {
	h.Broadcast(projectID, EventEnvelope{
		EventType: EventMetricSnapshot,
		ProjectID: projectID,
		Timestamp: time.Now().UTC(),
		Data:      snapshot,
	})
}

// BroadcastAnomaly helper for anomaly_alert event.
func (h *Hub) BroadcastAnomaly(projectID string, anom AnomalyPayload) {
	h.Broadcast(projectID, EventEnvelope{
		EventType: EventAnomalyAlert,
		ProjectID: projectID,
		Timestamp: time.Now().UTC(),
		Data:      anom,
	})
}
