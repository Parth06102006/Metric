package metrics

import (
	"context"
	"testing"
	"time"
)

func TestRunEmitsSnapshot(t *testing.T) {
	buffer := NewBuffer(60 * time.Second)
	buffer.Add(LogRecord{
		Ts: time.Now(), Status: 200, LatencyMs: 100,
		IP: "127.0.0.1", Endpoint: "/health",
	})

	ctx, cancel := context.WithCancel(context.Background())
	output := make(chan Snapshot, 1)

	go Run(ctx, buffer, 60, 10*time.Millisecond, output)

	select {
	case snapshot := <-output:
		if snapshot.RPS <= 0 {
			t.Errorf("RPS = %v, want > 0", snapshot.RPS)
		}
		cancel()
	case <-time.After(time.Second):
		cancel()
		t.Fatal("engine did not emit a snapshot within 1 second")
	}
}
