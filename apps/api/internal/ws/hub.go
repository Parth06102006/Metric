package ws

import (
	"encoding/json"
	"sync"
	"time"

	"traffic-center/domain"
)

// EventType identifies the payload carried by an envelope.
type EventType string

const (
	// EventMetricSnapshot carries a domain.Snapshot.
	EventMetricSnapshot EventType = "metric_snapshot"
	// EventAnomalyAlert carries a domain.Anomaly.
	EventAnomalyAlert EventType = "anomaly_alert"
)

// Envelope is the stable wire format for every WebSocket message.
type Envelope struct {
	EventType EventType `json:"event_type"`
	ProjectID string    `json:"project_id"`
	Timestamp time.Time `json:"timestamp"`
	Data      any       `json:"data"`
}

// Client is one subscriber. done is closed exactly once; send is never closed,
// which removes the send-on-closed-channel race entirely.
type Client struct {
	send chan []byte
	done chan struct{}
	once sync.Once
}

func newClient(buffer int) *Client {
	return &Client{
		send: make(chan []byte, buffer),
		done: make(chan struct{}),
	}
}

func (c *Client) close() {
	c.once.Do(func() { close(c.done) })
}

// Send is the outbound message stream.
func (c *Client) Send() <-chan []byte {
	return c.send
}

// Done is closed when the client is unregistered or its reader fails.
func (c *Client) Done() <-chan struct{} {
	return c.done
}

// Hub fans out events to subscribers grouped by project.
type Hub struct {
	// Serializes ownership/deletion changes against subscription registration
	// and outbound writes, so a revoked subscriber cannot receive later events.
	accessMu    sync.RWMutex
	mu          sync.RWMutex
	subscribers map[string]map[*Client]struct{}
}

// NewHub creates an empty hub.
func NewHub() *Hub {
	return &Hub{subscribers: make(map[string]map[*Client]struct{})}
}

// LockStreamAccess fences subscription changes and writes while project
// ownership or existence changes. Callers must release it promptly.
func (h *Hub) LockStreamAccess() { h.accessMu.Lock() }

// UnlockStreamAccess releases the project stream access fence.
func (h *Hub) UnlockStreamAccess() { h.accessMu.Unlock() }

// Register adds a client and returns it.
func (h *Hub) Register(projectID string) *Client {
	h.mu.Lock()
	defer h.mu.Unlock()

	client := newClient(64)
	if _, exists := h.subscribers[projectID]; !exists {
		h.subscribers[projectID] = make(map[*Client]struct{})
	}
	h.subscribers[projectID][client] = struct{}{}
	return client
}

// Unregister removes a client and signals its writer to stop.
func (h *Hub) Unregister(projectID string, client *Client) {
	if client == nil {
		return
	}
	client.close()

	h.mu.Lock()
	defer h.mu.Unlock()

	if subscribers, exists := h.subscribers[projectID]; exists {
		delete(subscribers, client)
		if len(subscribers) == 0 {
			delete(h.subscribers, projectID)
		}
	}
}

// SubscriberCount reports active subscribers for a project.
func (h *Hub) SubscriberCount(projectID string) int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.subscribers[projectID])
}

// Drop disconnects every subscriber of a project, e.g. after project deletion.
// Clients observe their done channel closing; the handler loops exit.
func (h *Hub) Drop(projectID string) {
	h.mu.Lock()

	subscribers, exists := h.subscribers[projectID]
	if exists {
		delete(h.subscribers, projectID)
	}
	h.mu.Unlock()

	for client := range subscribers {
		client.close()
	}
}

// Broadcast marshals once and delivers to every subscriber of a project.
// Slow clients are dropped rather than blocking the pipeline.
func (h *Hub) Broadcast(projectID string, event Envelope) {
	message, err := json.Marshal(event)
	if err != nil {
		return
	}

	h.mu.RLock()
	clients := make([]*Client, 0, len(h.subscribers[projectID]))
	for client := range h.subscribers[projectID] {
		clients = append(clients, client)
	}
	h.mu.RUnlock()

	for _, client := range clients {
		select {
		case client.send <- message:
		case <-client.done:
		default:
			// Client is full: drop this event instead of stalling the worker.
		}
	}
}

// BroadcastSnapshot implements domain.Broadcaster.
func (h *Hub) BroadcastSnapshot(projectID string, snapshot domain.Snapshot) {
	h.Broadcast(projectID, Envelope{
		EventType: EventMetricSnapshot,
		ProjectID: projectID,
		Timestamp: time.Now().UTC(),
		Data:      snapshot,
	})
}

// BroadcastAnomaly implements domain.Broadcaster.
func (h *Hub) BroadcastAnomaly(projectID string, anomaly domain.Anomaly) {
	h.Broadcast(projectID, Envelope{
		EventType: EventAnomalyAlert,
		ProjectID: projectID,
		Timestamp: time.Now().UTC(),
		Data:      anomaly,
	})
}
