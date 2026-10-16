package ws

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"traffic-center/backend/metrics"
	"traffic-center/backend/store"
)

type mockBufferProvider struct {
	buffers map[string]*metrics.Buffer
}

func (m *mockBufferProvider) GetBuffer(projectID string) (*metrics.Buffer, bool) {
	b, ok := m.buffers[projectID]
	return b, ok
}

func setupWSTestEnvironment(t *testing.T) (*Handler, *Hub, *store.Store, *mockBufferProvider) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "ws_test.db")
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	hub := NewHub()
	bp := &mockBufferProvider{
		buffers: make(map[string]*metrics.Buffer),
	}

	handler := &Handler{
		Store:   s,
		Buffers: bp,
		Hub:     hub,
	}

	return handler, hub, s, bp
}

func TestHub_ClientRegistrationAndBroadcast(t *testing.T) {
	hub := NewHub()
	projectID := "proj-test-1"

	client := hub.Register(projectID)
	if hub.SubscriberCount(projectID) != 1 {
		t.Fatalf("expected 1 subscriber, got %d", hub.SubscriberCount(projectID))
	}

	// Test broadcast metric snapshot
	snap := metrics.Snapshot{
		RPS:       25.0,
		P95:       120.0,
		ErrorRate: 2.0,
	}
	hub.BroadcastSnapshot(projectID, snap)

	select {
	case msg := <-client.send:
		var env EventEnvelope
		if err := json.Unmarshal(msg, &env); err != nil {
			t.Fatalf("unmarshal envelope: %v", err)
		}
		if env.EventType != EventMetricSnapshot {
			t.Errorf("expected EventMetricSnapshot, got %s", env.EventType)
		}
		if env.ProjectID != projectID {
			t.Errorf("expected project ID %s, got %s", projectID, env.ProjectID)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for metric snapshot event")
	}

	// Test broadcast anomaly alert
	anom := AnomalyPayload{
		Type:      "high_latency",
		Severity:  "high",
		Message:   "Latency breached threshold",
		Value:     600.0,
		Threshold: 500.0,
		Analysis:  "P95 latency spiked to 600ms",
	}
	hub.BroadcastAnomaly(projectID, anom)

	select {
	case msg := <-client.send:
		var env EventEnvelope
		if err := json.Unmarshal(msg, &env); err != nil {
			t.Fatalf("unmarshal envelope: %v", err)
		}
		if env.EventType != EventAnomalyAlert {
			t.Errorf("expected EventAnomalyAlert, got %s", env.EventType)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for anomaly alert event")
	}

	// Unregister
	hub.Unregister(projectID, client)
	if hub.SubscriberCount(projectID) != 0 {
		t.Fatalf("expected 0 subscribers after unregister, got %d", hub.SubscriberCount(projectID))
	}
}

func TestWSHandler_ClientConnectionReceivesInitialSnapshotAndBroadcasts(t *testing.T) {
	handler, hub, s, _ := setupWSTestEnvironment(t)

	proj, _, err := s.CreateProject("WS Project")
	if err != nil {
		t.Fatalf("CreateProject failed: %v", err)
	}

	server := httptest.NewServer(handler)
	defer server.Close()

	// Convert http URL to ws URL
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws/" + proj.ID

	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("WebSocket dial failed: %v", err)
	}
	defer conn.Close()

	// 1. Initial snapshot received upon connecting
	var initialEnv EventEnvelope
	if err := conn.ReadJSON(&initialEnv); err != nil {
		t.Fatalf("read initial event failed: %v", err)
	}
	if initialEnv.EventType != EventMetricSnapshot {
		t.Fatalf("expected initial event to be %s, got %s", EventMetricSnapshot, initialEnv.EventType)
	}
	if initialEnv.ProjectID != proj.ID {
		t.Fatalf("expected project ID %s, got %s", proj.ID, initialEnv.ProjectID)
	}

	// 2. Hub broadcast of anomaly event is received by client
	anom := AnomalyPayload{
		Type:      "high_error_rate",
		Severity:  "critical",
		Message:   "500s detected",
		Value:     15.0,
		Threshold: 5.0,
		Analysis:  "Server error rate elevated",
	}
	hub.BroadcastAnomaly(proj.ID, anom)

	var alertEnv EventEnvelope
	if err := conn.ReadJSON(&alertEnv); err != nil {
		t.Fatalf("read anomaly alert failed: %v", err)
	}
	if alertEnv.EventType != EventAnomalyAlert {
		t.Fatalf("expected event %s, got %s", EventAnomalyAlert, alertEnv.EventType)
	}
}
