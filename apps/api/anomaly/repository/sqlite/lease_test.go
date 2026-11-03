package sqlite_test

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

func TestAnalysisJobLeaseAndCompletion(t *testing.T) {
	ctx := context.Background()
	db, err := storesqlite.Open(filepath.Join(t.TempDir(), "lease.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := anomalysqlite.New(db)
	a := domain.Anomaly{Type: "high_latency", Severity: "high", Message: "slow", DetectedAt: time.Now()}
	if err := repo.SaveWithAnalysis(ctx, "p", &a, domain.Snapshot{}, 10); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	job, err := repo.ClaimAnalysis(ctx, now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = repo.ClaimAnalysis(ctx, now, time.Minute); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("job claimed twice: %v", err)
	}
	recovered, err := repo.ClaimAnalysis(ctx, now.Add(2*time.Minute), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if job.LeaseToken == recovered.LeaseToken {
		t.Fatal("lease token reused")
	}
	if err = repo.ReleaseAnalysis(ctx, job); err == nil {
		t.Fatal("stale lease released a job claimed by another worker")
	}
	d := domain.Diagnosis{Pattern: "latency_degradation", Direction: "latency", Model: "test"}
	if err = repo.CompleteAnalysis(ctx, job, d, "advisory"); err == nil {
		t.Fatal("stale lease completed job")
	}
	if err = repo.CompleteAnalysis(ctx, recovered, d, "advisory"); err != nil {
		t.Fatal(err)
	}
	items, err := repo.List(ctx, "p", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].AnalysisStatus != "completed" || items[0].Diagnosis == nil || items[0].Diagnosis.Pattern != "latency_degradation" {
		t.Fatalf("unexpected stored incident: %+v", items)
	}
	if _, err = repo.ClaimAnalysis(ctx, now.Add(3*time.Minute), time.Minute); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("completed job claimed again: %v", err)
	}
}

func TestAnalysisFailureAndDeleteDoNotLeaveJobs(t *testing.T) {
	ctx := context.Background()
	db, err := storesqlite.Open(filepath.Join(t.TempDir(), "failed.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := anomalysqlite.New(db)
	a := domain.Anomaly{Type: "x", Severity: "high", Message: "rule", DetectedAt: time.Now()}
	if err = repo.SaveWithAnalysis(ctx, "p", &a, domain.Snapshot{}, 5); err != nil {
		t.Fatal(err)
	}
	job, err := repo.ClaimAnalysis(ctx, time.Now(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err = repo.FailAnalysis(ctx, job, time.Now(), true); err != nil {
		t.Fatal(err)
	}
	items, err := repo.List(ctx, "p", 10)
	if err != nil || items[0].AnalysisStatus != "failed" {
		t.Fatalf("failed status: %+v %v", items, err)
	}
	a.ID = 0
	if err = repo.SaveWithAnalysis(ctx, "p", &a, domain.Snapshot{}, 5); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`DELETE FROM anomalies WHERE id=?`, a.ID); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = db.QueryRow(`SELECT COUNT(*) FROM analysis_jobs`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("orphaned job count=%d err=%v", count, err)
	}
}
