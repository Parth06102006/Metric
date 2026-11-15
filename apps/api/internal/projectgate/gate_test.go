package projectgate

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestGateIsolationCancellationAndCleanup(t *testing.T) {
	g := New()
	release, err := g.Lock(context.Background(), "busy")
	if err != nil {
		t.Fatal(err)
	}
	other, err := g.Lock(context.Background(), "other")
	if err != nil {
		t.Fatal(err)
	}
	other()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if unlock, err := g.Lock(ctx, "busy"); !errors.Is(err, context.DeadlineExceeded) {
		if unlock != nil {
			unlock()
		}
		t.Fatalf("wait = %v", err)
	}
	release()
	release()
	if len(g.entries) != 0 {
		t.Fatalf("leaked entries: %d", len(g.entries))
	}
	// A cancelled request must not take an available lock either.
	if unlock, err := g.Lock(ctx, "new"); err == nil {
		unlock()
		t.Fatal("cancelled acquisition succeeded")
	}
	if len(g.entries) != 0 {
		t.Fatal("cancelled acquisition leaked entry")
	}
}
