package ws

import (
	"errors"
	"net"
	"testing"
	"time"

	"traffic-center/domain"
)

func TestOverflowClosesSocketAndReconnects(t *testing.T) {
	f := newExpiryFixture(t, time.Minute, nil)
	conn := f.dial(t)
	readInitialSnapshot(t, conn)
	// Keep the writer from draining the queue while broadcasts arrive. It may
	// dequeue one message before waiting on the fence; 66 always overflows 64.
	f.hub.LockStreamAccess()
	for i := 0; i < 66; i++ {
		f.hub.BroadcastAnomaly(expiryTestProject, domain.Anomaly{ID: int64(i + 1)})
	}
	f.hub.UnlockStreamAccess()
	if f.hub.SubscriberCount(expiryTestProject) != 0 {
		t.Fatal("overflowed client still registered")
	}
	_, _, err := conn.ReadMessage()
	if err == nil {
		t.Fatal("queued data delivered after overflow")
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		t.Fatal("overflow did not close socket")
	}
	f.awaitCleanup(t)
	next := f.dial(t)
	readInitialSnapshot(t, next)
	_ = next.Close()
	f.awaitCleanup(t)
}
