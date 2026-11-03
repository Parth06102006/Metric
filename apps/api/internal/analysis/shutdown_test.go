package analysis

import (
	"context"
	"path/filepath"
	"testing"
	"time"
	anomalysqlite "traffic-center/anomaly/repository/sqlite"
	"traffic-center/domain"
	"traffic-center/internal/jev"

	storesqlite "traffic-center/internal/sqlite"
)

func TestCancellationBeforeJobProcessingKeepsJobRetryable(t *testing.T) {
	ctx := context.Background()
	db, err := storesqlite.Open(filepath.Join(t.TempDir(), "shutdown.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := anomalysqlite.New(db)
	a := domain.Anomaly{Type: "x", Severity: "high", Message: "rule", DetectedAt: time.Now()}
	if err = repo.SaveWithAnalysis(ctx, "p", &a, domain.Snapshot{}, 10); err != nil {
		t.Fatal(err)
	}
	job, err := repo.ClaimAnalysis(ctx, time.Now(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	w := NewWorker(repo, &countingAnalyst{}, nil)
	w.process(canceled, job)

	job, err = repo.ClaimAnalysis(ctx, time.Now(), time.Minute)
	if err != nil {
		t.Fatalf("job not immediately retryable after shutdown: %v", err)
	}
	if job.Attempts != 1 {
		t.Fatalf("a lease released before a provider call must not consume an attempt, got %d", job.Attempts)
	}
}

type cancelingAnalyst struct{ cancel context.CancelFunc }

func (a cancelingAnalyst) Diagnose(ctx context.Context, _ jev.State) (jev.Result, error) {
	a.cancel()
	return jev.Result{}, ctx.Err()
}

func TestCancellationDuringProviderCallKeepsAttempt(t *testing.T) {
	db, err := storesqlite.Open(filepath.Join(t.TempDir(), "shutdown.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := anomalysqlite.New(db)
	ctx := context.Background()
	a := domain.Anomaly{Type: "x", Severity: "high", Message: "rule", DetectedAt: time.Now()}
	if err := repo.SaveWithAnalysis(ctx, "p", &a, domain.Snapshot{}, 10); err != nil {
		t.Fatal(err)
	}
	job, err := repo.ClaimAnalysis(ctx, time.Now(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	processing, cancel := context.WithCancel(ctx)
	defer cancel()
	w := NewWorker(repo, cancelingAnalyst{cancel}, nil)
	w.process(processing, job)
	job, err = repo.ClaimAnalysis(ctx, time.Now(), time.Minute)
	if err != nil {
		t.Fatalf("job not immediately retryable: %v", err)
	}
	if job.Attempts != 2 {
		t.Fatalf("in-flight request must consume an attempt, got %d", job.Attempts)
	}
}
