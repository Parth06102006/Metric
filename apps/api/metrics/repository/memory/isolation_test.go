package memory

import (
	"context"
	"reflect"
	"testing"
	"time"
	"traffic-center/domain"
)

func TestBusyProjectDoesNotBlockOtherWindowsOrDiscovery(t *testing.T) {
	r := New(time.Minute)
	ctx := context.Background()
	_ = r.Append(ctx, "busy", []domain.LogRecord{{Ts: time.Now()}})
	p := r.lookup("busy", false)
	p.mu.Lock()
	defer p.mu.Unlock()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = r.Append(ctx, "other", []domain.LogRecord{{Ts: time.Now()}})
		_, _ = r.Recent(ctx, "other", time.Now().Add(-time.Minute))
		_ = r.ActiveProjects(ctx)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("other project blocked by busy window")
	}
}

func TestAppendSweepDeferralDoesNotChangeReadWindow(t *testing.T) {
	r := New(time.Minute)
	ctx := context.Background()
	_ = r.Append(ctx, "p", []domain.LogRecord{{Ts: time.Now(), Endpoint: "/old"}})
	p := r.lookup("p", false)
	p.mu.Lock()
	p.records[0].Ts = time.Now().Add(-2 * time.Minute)
	p.earliest = p.records[0].Ts
	p.nextAppendPrune = time.Now().Add(time.Hour) // deterministically defer sweep
	p.mu.Unlock()
	_ = r.Append(ctx, "p", []domain.LogRecord{{Ts: time.Now(), Endpoint: "/live"}})
	got, err := r.Recent(ctx, "p", time.Now().Add(-time.Minute))
	if err != nil || len(got) != 1 || got[0].Endpoint != "/live" {
		t.Fatalf("read included deferred expired data: %+v %v", got, err)
	}
}

func TestPruneOutOfOrderAndClearExpiredReferences(t *testing.T) {
	now := time.Now()
	r := New(time.Hour)
	ctx := context.Background()
	first := domain.LogRecord{Ts: now.Add(-30 * time.Minute), Endpoint: "/live"}
	second := domain.LogRecord{Ts: now.Add(-59 * time.Minute), Endpoint: "/expires-first"}
	_ = r.Append(ctx, "p", []domain.LogRecord{first, second})
	p := r.lookup("p", false)
	p.mu.Lock()
	p.prune(now.Add(-45 * time.Minute))
	if !reflect.DeepEqual(p.records, []domain.LogRecord{first}) {
		t.Fatalf("records: %+v", p.records)
	}
	if got := p.records[:cap(p.records)][1]; got.Endpoint != "" {
		t.Fatal("expired backing reference retained")
	}
	p.mu.Unlock()
	got, _ := r.Recent(ctx, "p", now.Add(-time.Hour))
	got[0].Endpoint = "mutated"
	again, _ := r.Recent(ctx, "p", now.Add(-time.Hour))
	if again[0].Endpoint != "/live" {
		t.Fatal("returned records alias storage")
	}
}
