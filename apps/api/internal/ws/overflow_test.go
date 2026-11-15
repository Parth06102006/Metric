package ws_test

import (
	"testing"
	"traffic-center/domain"
	"traffic-center/internal/ws"
)

func TestOverflowDisconnectsOnlySlowSubscriber(t *testing.T) {
	hub := ws.NewHub()
	slow := hub.Register("p")
	healthy := hub.Register("p")
	other := hub.Register("other")
	defer hub.Unregister("p", healthy)
	defer hub.Unregister("other", other)
	for i := 0; i < 65; i++ {
		hub.BroadcastAnomaly("p", domain.Anomaly{ID: int64(i + 1)})
		<-healthy.Send()
	}
	select {
	case <-slow.Done():
	default:
		t.Fatal("overflowed subscriber remained connected")
	}
	select {
	case <-healthy.Done():
		t.Fatal("healthy subscriber disconnected")
	default:
	}
	select {
	case <-other.Done():
		t.Fatal("other project disconnected")
	default:
	}
	if hub.SubscriberCount("p") != 1 {
		t.Fatal("slow subscriber not removed")
	}
	// Reconnect starts with an empty queue; a completed diagnosis reaches both
	// healthy clients. Durable history reconciliation is the frontend's job.
	fresh := hub.Register("p")
	defer hub.Unregister("p", fresh)
	hub.BroadcastAnomaly("p", domain.Anomaly{ID: 65, AnalysisStatus: "completed"})
	<-fresh.Send()
	<-healthy.Send()
	hub.Unregister("p", slow) // handler cleanup remains idempotent
}
