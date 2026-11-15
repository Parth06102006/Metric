package analysis

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
	anomalysqlite "traffic-center/anomaly/repository/sqlite"
	"traffic-center/domain"

	storesqlite "traffic-center/internal/sqlite"
)

func TestCancellationBeforeJobProcessingKeepsJobRetryable(t *testing.T) {
	ctx := context.Background()
	db, err := storesqlite.Open(filepath.Join(t.TempDir(), "shutdown.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO projects(id,name,api_key_hash) VALUES ('p','test','hash')`); err != nil {
		t.Fatal(err)
	}
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

func (a cancelingAnalyst) Diagnose(ctx context.Context, _ domain.IncidentEvidence) (domain.AnalysisResult, error) {
	a.cancel()
	return domain.AnalysisResult{}, ctx.Err()
}

func TestCancellationDuringProviderCallIsNotRetried(t *testing.T) {
	db, err := storesqlite.Open(filepath.Join(t.TempDir(), "shutdown.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO projects(id,name,api_key_hash) VALUES ('p','test','hash')`); err != nil {
		t.Fatal(err)
	}
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
	_, err = repo.ClaimAnalysis(ctx, time.Now(), time.Minute)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("in-flight request must not retry: %v", err)
	}
	rows, err := repo.List(ctx, "p", 10)
	if err != nil || len(rows) != 1 || rows[0].AnalysisStatus != "failed" {
		t.Fatalf("expected failed advisory: %+v %v", rows, err)
	}
}
