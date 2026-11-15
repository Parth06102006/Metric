package memory

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
	"traffic-center/domain"
)

func TestLimitsAtomicBatchAndRelease(t *testing.T) {
	r := NewWithLimits(time.Minute, Limits{ProjectRecords: 2, TotalRecords: 3, Projects: 2, ProjectBytes: 1000, TotalBytes: 2000})
	ctx := context.Background()
	a := domain.LogRecord{Ts: time.Now(), Endpoint: "/a"}
	if err := r.Append(ctx, "a", []domain.LogRecord{a, a}); err != nil {
		t.Fatal(err)
	}
	if err := r.Append(ctx, "a", []domain.LogRecord{a}); !errors.Is(err, domain.ErrCapacity) {
		t.Fatalf("project limit: %v", err)
	}
	if err := r.Append(ctx, "b", []domain.LogRecord{a, a}); !errors.Is(err, domain.ErrCapacity) {
		t.Fatalf("global batch limit: %v", err)
	}
	if got, _ := r.Recent(ctx, "b", time.Now().Add(-time.Minute)); len(got) != 0 {
		t.Fatal("rejected batch partially appended")
	}
	if err := r.Append(ctx, "b", []domain.LogRecord{a}); err != nil {
		t.Fatal(err)
	}
	if err := r.Append(ctx, "c", []domain.LogRecord{a}); !errors.Is(err, domain.ErrCapacity) {
		t.Fatal("project entry limit ignored")
	}
	if err := r.Delete(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if err := r.Append(ctx, "c", []domain.LogRecord{a, a}); err != nil {
		t.Fatalf("capacity not released: %v", err)
	}
}
func TestByteLimitsAndExpirationRelease(t *testing.T) {
	a := domain.LogRecord{Ts: time.Now(), Endpoint: "/hello"}
	n := recordBytes(a)
	r := NewWithLimits(time.Minute, Limits{ProjectRecords: 10, TotalRecords: 10, Projects: 10, ProjectBytes: n, TotalBytes: n})
	if err := r.Append(context.Background(), "a", []domain.LogRecord{a}); err != nil {
		t.Fatal(err)
	}
	if err := r.Append(context.Background(), "b", []domain.LogRecord{a}); !errors.Is(err, domain.ErrCapacity) {
		t.Fatalf("total bytes: %v", err)
	}
	p := r.lookup("a", false)
	p.mu.Lock()
	p.records[0].Ts = time.Now().Add(-2 * time.Minute)
	p.earliest = p.records[0].Ts
	p.prune(time.Now().Add(-time.Minute))
	p.mu.Unlock()
	if err := r.Append(context.Background(), "b", []domain.LogRecord{a}); err != nil {
		t.Fatalf("expired bytes not released: %v", err)
	}
}
func TestEndpointCardinalityAtomicAndReclaimsExpired(t *testing.T) {
	r := NewWithLimits(time.Minute, Limits{ProjectRecords: 10, TotalRecords: 10, Projects: 10, ProjectEndpoints: 2, ProjectBytes: 100000, TotalBytes: 100000})
	ctx := context.Background()
	now := time.Now()
	if err := r.Append(ctx, "p", []domain.LogRecord{{Ts: now, Endpoint: "/a"}, {Ts: now, Endpoint: "/b"}}); err != nil {
		t.Fatal(err)
	}
	if err := r.Append(ctx, "p", []domain.LogRecord{{Ts: now, Endpoint: "/a"}, {Ts: now, Endpoint: "/c"}}); !errors.Is(err, domain.ErrCapacity) {
		t.Fatalf("cardinality: %v", err)
	}
	got, _ := r.Recent(ctx, "p", now.Add(-time.Minute))
	if len(got) != 2 {
		t.Fatal("partial batch accepted")
	}
	p := r.lookup("p", false)
	p.mu.Lock()
	p.records[0].Ts = now.Add(-2 * time.Minute)
	p.earliest = p.records[0].Ts
	p.mu.Unlock()
	if err := r.Append(ctx, "p", []domain.LogRecord{{Ts: now, Endpoint: "/c"}}); err != nil {
		t.Fatalf("expired endpoint not reclaimed: %v", err)
	}
}
func TestPruneShrinksExpiredBackingStorage(t *testing.T) {
	r := New(time.Minute)
	ctx := context.Background()
	batch := make([]domain.LogRecord, 1000)
	for i := range batch {
		batch[i] = domain.LogRecord{Ts: time.Now(), Endpoint: "/x"}
	}
	if err := r.Append(ctx, "p", batch); err != nil {
		t.Fatal(err)
	}
	p := r.lookup("p", false)
	p.mu.Lock()
	for i := 1; i < len(p.records); i++ {
		p.records[i].Ts = time.Now().Add(-time.Hour)
	}
	p.earliest = time.Now().Add(-time.Hour)
	p.prune(time.Now().Add(-time.Minute))
	if len(p.records) != 1 || cap(p.records) > 64 {
		t.Fatalf("retained backing storage: len %d cap %d", len(p.records), cap(p.records))
	}
	p.mu.Unlock()
}
func TestExpiredBatchRejectedAtomicallyAfterLockWait(t *testing.T) {
	r := New(time.Second)
	ctx := context.Background()
	if err := r.Append(ctx, "p", []domain.LogRecord{{Ts: time.Now(), Endpoint: "/existing"}}); err != nil {
		t.Fatal(err)
	}
	p := r.lookup("p", false)
	p.mu.Lock()
	batch := []domain.LogRecord{{Ts: time.Now(), Endpoint: "/fresh"}, {Ts: time.Now().Add(-900 * time.Millisecond), Endpoint: "/about-to-expire"}}
	done := make(chan error, 1)
	go func() { done <- r.Append(ctx, "p", batch) }()
	time.Sleep(150 * time.Millisecond)
	p.mu.Unlock()
	if err := <-done; !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("expired batch accepted: %v", err)
	}
	got, err := r.Recent(ctx, "p", time.Now().Add(-time.Second))
	if err != nil || len(got) != 1 || got[0].Endpoint != "/existing" {
		t.Fatalf("partial batch retained: %+v %v", got, err)
	}
}
func TestConcurrentGlobalLimit(t *testing.T) {
	r := NewWithLimits(time.Minute, Limits{ProjectRecords: 20, TotalRecords: 20, Projects: 50, ProjectBytes: 100000, TotalBytes: 100000})
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = r.Append(context.Background(), string(rune('a'+i)), []domain.LogRecord{{Ts: time.Now()}})
		}(i)
	}
	wg.Wait()
	r.budgetMu.Lock()
	defer r.budgetMu.Unlock()
	if r.totalRecords != 20 {
		t.Fatalf("retained %d, want 20", r.totalRecords)
	}
}
func TestFutureTimestampRejectedOrClamped(t *testing.T) {
	r := New(time.Minute)
	ctx := context.Background()
	now := time.Now()
	if err := r.Append(ctx, "p", []domain.LogRecord{{Ts: now.Add(time.Hour)}}); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("future: %v", err)
	}
	if err := r.Append(ctx, "p", []domain.LogRecord{{Ts: now.Add(time.Second)}}); err != nil {
		t.Fatal(err)
	}
	got, _ := r.Recent(ctx, "p", now.Add(-time.Minute))
	if len(got) != 1 || got[0].Ts.After(time.Now()) {
		t.Fatal("small clock skew was not clamped")
	}
}
