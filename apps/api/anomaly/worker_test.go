package anomaly

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"traffic-center/domain"
	"traffic-center/internal/projectgate"
	"traffic-center/metrics"
	memory "traffic-center/metrics/repository/memory"
	"traffic-center/project"
	rowmemory "traffic-center/project/repository/memory"
)

type blockingSnapshots struct {
	started chan string
	release chan struct{}
	active  atomic.Int32
	peak    atomic.Int32
}

func (s *blockingSnapshots) Current(ctx context.Context, id string) (domain.Snapshot, error) {
	n := s.active.Add(1)
	defer s.active.Add(-1)
	for {
		old := s.peak.Load()
		if n <= old || s.peak.CompareAndSwap(old, n) {
			break
		}
	}
	s.started <- id
	select {
	case <-s.release:
		return domain.Snapshot{}, nil
	case <-ctx.Done():
		return domain.Snapshot{}, ctx.Err()
	}
}

type countedStore struct {
	domain.SnapshotRepository
	count atomic.Int32
}

func (s *countedStore) Save(context.Context, string, domain.Snapshot) error {
	s.count.Add(1)
	return nil
}

func TestWorkerBoundedParallelProjects(t *testing.T) {
	ctx := context.Background()
	projects := rowmemory.New()
	records := memory.New(time.Minute)
	for i := 0; i < 8; i++ {
		id := fmt.Sprint(i)
		_ = projects.Create(ctx, domain.Project{ID: id}, "hash")
		_ = records.Append(ctx, id, []domain.LogRecord{{Ts: time.Now()}})
	}
	snapshots := &blockingSnapshots{started: make(chan string, 8), release: make(chan struct{})}
	store := &countedStore{}
	w := NewWorker(records, snapshots, store, nil, nil, time.Second, 0, slog.New(slog.NewTextHandler(io.Discard, nil)), projects, projectgate.New())
	done := make(chan struct{})
	go func() { defer close(done); w.EvaluateAll(ctx) }()
	for i := 0; i < 4; i++ {
		select {
		case <-snapshots.started:
		case <-time.After(time.Second):
			t.Fatal("one project blocked other workers")
		}
	}
	if snapshots.peak.Load() != 4 {
		t.Fatalf("parallelism=%d", snapshots.peak.Load())
	}
	close(snapshots.release)
	<-done
	if store.count.Load() != 8 || snapshots.peak.Load() > 4 {
		t.Fatalf("saved=%d peak=%d", store.count.Load(), snapshots.peak.Load())
	}
}

func TestDeletionWaitsForWorkerAndStaleIDCannotWrite(t *testing.T) {
	ctx := domain.WithIdentity(context.Background(), domain.Identity{Email: "admin@example.com", Admin: true})
	projects := rowmemory.New()
	records := memory.New(time.Minute)
	gate := projectgate.New()
	_ = projects.Create(ctx, domain.Project{ID: "p"}, "hash")
	snapshots := &blockingSnapshots{started: make(chan string, 1), release: make(chan struct{})}
	store := &countedStore{}
	w := NewWorker(records, snapshots, store, nil, nil, time.Second, 0, nil, projects, gate)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); w.EvaluateProject(ctx, "p") }()
	<-snapshots.started
	lifecycle := project.NewLifecycle(projects, records, nil, gate)
	wait, cancel := context.WithTimeout(ctx, 30*time.Millisecond)
	defer cancel()
	if err := lifecycle.DeleteProject(wait, "p"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("delete while compute blocked: %v", err)
	}
	close(snapshots.release)
	wg.Wait()
	if err := lifecycle.DeleteProject(ctx, "p"); err != nil {
		t.Fatal(err)
	}
	w.EvaluateProject(ctx, "p") // stale ActiveProjects result
	if store.count.Load() != 1 {
		t.Fatal("worker wrote after deletion")
	}
	select {
	case <-snapshots.started:
		t.Fatal("deleted project recomputed")
	default:
	}
}

func BenchmarkSnapshotCycle(b *testing.B) {
	for _, parallelism := range []int{1, 4} {
		b.Run(fmt.Sprintf("workers=%d", parallelism), func(b *testing.B) {
			ctx := context.Background()
			projects := rowmemory.New()
			records := memory.New(time.Hour)
			batch := make([]domain.LogRecord, 6000)
			for i := range batch {
				batch[i] = domain.LogRecord{Ts: time.Now(), IP: fmt.Sprint(i % 200), Endpoint: fmt.Sprint(i % 20), LatencyMs: float64(i % 1000), Status: 200}
			}
			for i := 0; i < 8; i++ {
				id := fmt.Sprint(i)
				_ = projects.Create(ctx, domain.Project{ID: id}, "hash")
				_ = records.Append(ctx, id, batch)
			}
			w := NewWorker(records, metrics.NewUsecase(records, nil, time.Hour), nil, nil, nil, time.Second, 0, slog.New(slog.NewTextHandler(io.Discard, nil)), projects, projectgate.New())
			w.parallelism = parallelism
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				w.EvaluateAll(ctx)
			}
		})
	}
}
