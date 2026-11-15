package analysis

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	anomalysqlite "traffic-center/anomaly/repository/sqlite"
	"traffic-center/domain"
	storesqlite "traffic-center/internal/sqlite"
)

func TestDailyBudgetPersistsAndSkipsExcess(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "budget.db")
	db, err := storesqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO projects(id,name,api_key_hash) VALUES('p','test','hash')`); err != nil {
		t.Fatal(err)
	}
	repo := anomalysqlite.New(db)
	provider := &countingAnalyst{}
	worker := NewWorker(repo, provider, nil, 1)
	a := domain.Anomaly{Type: "x", Severity: "high", Message: "rule", DetectedAt: time.Now()}
	if err := repo.SaveWithAnalysis(ctx, "p", &a, domain.Snapshot{}, 10); err != nil {
		t.Fatal(err)
	}
	job, err := repo.ClaimAnalysis(ctx, time.Now(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	worker.process(ctx, job)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = storesqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	repo = anomalysqlite.New(db)
	worker = NewWorker(repo, provider, nil, 1)
	b := domain.Anomaly{Type: "y", Severity: "high", Message: "rule", DetectedAt: time.Now()}
	if err := repo.SaveWithAnalysis(ctx, "p", &b, domain.Snapshot{}, 10); err != nil {
		t.Fatal(err)
	}
	job, err = repo.ClaimAnalysis(ctx, time.Now(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	worker.process(ctx, job)
	rows, err := repo.List(ctx, "p", 10)
	if err != nil || len(rows) != 2 {
		t.Fatal(rows, err)
	}
	var skipped bool
	for _, v := range rows {
		if v.ID == b.ID && v.AnalysisStatus == "skipped" {
			skipped = true
		}
	}
	if !skipped || provider.calls.Load() != 1 {
		t.Fatalf("budget failed: %+v calls=%d", rows, provider.calls.Load())
	}
}
func TestDailyBudgetConcurrentReservations(t *testing.T) {
	ctx := context.Background()
	db, err := storesqlite.Open(filepath.Join(t.TempDir(), "concurrent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(`INSERT INTO projects(id,name,api_key_hash) VALUES('p','test','hash')`); err != nil {
		t.Fatal(err)
	}
	repo := anomalysqlite.New(db)
	jobs := make([]anomalysqlite.AnalysisJob, 0, 10)
	for i := 0; i < 10; i++ {
		a := domain.Anomaly{Type: "x", Message: "rule", DetectedAt: time.Now()}
		if err := repo.SaveWithAnalysis(ctx, "p", &a, domain.Snapshot{}, 20); err != nil {
			t.Fatal(err)
		}
		job, err := repo.ClaimAnalysis(ctx, time.Now(), time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		jobs = append(jobs, job)
	}
	provider := &countingAnalyst{}
	worker := NewWorker(repo, provider, nil, 3)
	var wg sync.WaitGroup
	for _, job := range jobs {
		wg.Add(1)
		go func(j anomalysqlite.AnalysisJob) { defer wg.Done(); worker.process(ctx, j) }(job)
	}
	wg.Wait()
	if provider.calls.Load() != 3 {
		t.Fatalf("daily cap exceeded or underused: %d", provider.calls.Load())
	}
	var skipped int
	if err := db.QueryRow(`SELECT count(*) FROM anomalies WHERE analysis_status='skipped'`).Scan(&skipped); err != nil || skipped != 7 {
		t.Fatalf("skipped=%d err=%v", skipped, err)
	}
}

func TestExpiredDispatchedCallIsNotReclaimed(t *testing.T) {
	ctx := context.Background()
	db, err := storesqlite.Open(filepath.Join(t.TempDir(), "crash.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(`INSERT INTO projects(id,name,api_key_hash) VALUES('p','test','hash')`); err != nil {
		t.Fatal(err)
	}
	repo := anomalysqlite.New(db)
	a := domain.Anomaly{Type: "x", Message: "rule", DetectedAt: time.Now()}
	if err := repo.SaveWithAnalysis(ctx, "p", &a, domain.Snapshot{}, 10); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	job, err := repo.ClaimAnalysis(ctx, now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	ok, err := repo.BeginAnalysisCall(ctx, job, now, 1)
	if err != nil || !ok {
		t.Fatalf("reserve %v %v", ok, err)
	}
	if _, err := repo.ClaimAnalysis(ctx, now.Add(2*time.Minute), time.Minute); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("reclaimed dispatched job: %v", err)
	}
	rows, err := repo.List(ctx, "p", 10)
	if err != nil || len(rows) != 1 || rows[0].AnalysisStatus != "failed" {
		t.Fatalf("uncertain outcome: %+v %v", rows, err)
	}
}
