package metrics

import (
	"testing"
	"time"
)

func TestBufferWindow(t *testing.T) {
	now := time.Now()
	buffer := NewBuffer(60 * time.Second)

	buffer.Add(LogRecord{Ts: now.Add(-70 * time.Second)})
	buffer.Add(LogRecord{Ts: now.Add(-30 * time.Second)})
	buffer.Add(LogRecord{Ts: now})

	got := buffer.Window(now)

	if len(got) != 2 {
		t.Fatalf("got %d records, want 2", len(got))
	}
}
