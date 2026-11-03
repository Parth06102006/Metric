package analysis

import (
	"context"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	anomalysqlite "traffic-center/anomaly/repository/sqlite"
	"traffic-center/domain"
	"traffic-center/internal/jev"
	storesqlite "traffic-center/internal/sqlite"
)

type transientAnalyst struct {
	calls  atomic.Int64
	failed chan time.Time
}

func (a *transientAnalyst) Diagnose(context.Context, jev.State) (jev.Result, error) {
	if a.calls.Add(1) == 1 {
		a.failed <- time.Now()
		return jev.Result{}, errors.New("simulated transient provider outage")
	}
	return jev.Result{Pattern: "unclear", Direction: "more_evidence", Model: "recovery-fake"}, nil
}

type recoveryUpdates struct{ events chan domain.Anomaly }

func (u *recoveryUpdates) BroadcastSnapshot(string, domain.Snapshot)   {}
func (u *recoveryUpdates) BroadcastAnomaly(_ string, a domain.Anomaly) { u.events <- a }
func (u *recoveryUpdates) Drop(string)                                 {}

// This intentionally exercises the real worker's 30-second provider rate
// limit rather than fast-forwarding its clock or shortening production delays.
func TestProviderFailureRecoveryWithRealRetry(t *testing.T) {
	ctx := context.Background()
	db, err := storesqlite.Open(filepath.Join(t.TempDir(), "retry.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := anomalysqlite.New(db)
	a := domain.Anomaly{Type: "high_error_rate", Severity: "critical", Message: "rule alert", Analysis: "Rule-based alert: rule alert", DetectedAt: time.Now()}
	if err := repo.SaveWithAnalysis(ctx, "p", &a, domain.Snapshot{}, 10); err != nil {
		t.Fatal(err)
	}
	provider := &transientAnalyst{failed: make(chan time.Time, 1)}
	updates := &recoveryUpdates{events: make(chan domain.Anomaly, 1)}
	worker := NewWorker(repo, provider, nil)
	worker.SetBroadcaster(updates)
	worker.Start()
	defer worker.Stop()
	var failedAt time.Time
	select {
	case failedAt = <-provider.failed:
	case <-time.After(10 * time.Second):
		t.Fatal("first provider call did not happen")
	}
	// The incident must remain available while the provider is unavailable.
	rows, err := repo.List(ctx, "p", 10)
	if err != nil || len(rows) != 1 || rows[0].ID != a.ID || rows[0].AnalysisStatus != "pending" || rows[0].Analysis == "" {
		t.Fatalf("pending alert lost: %+v, %v", rows, err)
	}
	select {
	case updated := <-updates.events:
		if updated.ID != a.ID || updated.AnalysisStatus != "completed" {
			t.Fatalf("bad recovery update: %+v", updated)
		}
	case <-time.After(45 * time.Second):
		t.Fatal("worker did not recover after transient failure")
	}
	recovery := time.Since(failedAt)
	rows, err = repo.List(ctx, "p", 10)
	if err != nil || len(rows) != 1 || rows[0].ID != a.ID || rows[0].AnalysisStatus != "completed" || rows[0].Diagnosis == nil {
		t.Fatalf("recovered incident: %+v, %v", rows, err)
	}
	if provider.calls.Load() != 2 {
		t.Fatalf("provider calls = %d, want 2", provider.calls.Load())
	}
	var jobs int
	if err := db.QueryRow(`SELECT COUNT(*) FROM analysis_jobs WHERE anomaly_id = ?`, a.ID).Scan(&jobs); err != nil || jobs != 0 {
		t.Fatalf("remaining jobs: %d, %v", jobs, err)
	}
	t.Logf("provider recovery: %s; calls: %d; retries: %d; duplicate incidents: %d", recovery.Round(time.Millisecond), provider.calls.Load(), provider.calls.Load()-1, len(rows)-1)
}

// When the result cannot be committed, the incident survives and the expired
// lease can be retried. The second provider call illustrates the billing risk.
func TestRecoveryAfterDatabaseFailureDuringCommit(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "commit-failure.db")
	db, err := storesqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	repo := anomalysqlite.New(db)
	a := domain.Anomaly{Type: "high_error_rate", Severity: "high", Message: "rule alert", Analysis: "rule alert", DetectedAt: time.Now()}
	if err := repo.SaveWithAnalysis(ctx, "p", &a, domain.Snapshot{}, 10); err != nil {
		db.Close()
		t.Fatal(err)
	}
	job, err := repo.ClaimAnalysis(ctx, time.Now(), time.Minute)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	provider := &countingAnalyst{}
	NewWorker(repo, provider, nil).process(ctx, job)
	if provider.calls.Load() != 1 {
		t.Fatal("expected provider call before failed commit")
	}
	db, err = storesqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo = anomalysqlite.New(db)
	rows, err := repo.List(ctx, "p", 10)
	if err != nil || len(rows) != 1 || rows[0].AnalysisStatus != "pending" {
		t.Fatalf("alert missing after failure: %+v, %v", rows, err)
	}
	// Simulate passage of the one-minute lease; this is not a wall-clock recovery measurement.
	recovered, err := repo.ClaimAnalysis(ctx, time.Now().Add(2*time.Minute), time.Minute)
	if err != nil || recovered.Attempts != 2 {
		t.Fatalf("lease not retryable: %+v, %v", recovered, err)
	}
	NewWorker(repo, provider, nil).process(ctx, recovered)
	rows, err = repo.List(ctx, "p", 10)
	if err != nil || len(rows) != 1 || rows[0].ID != a.ID || rows[0].AnalysisStatus != "completed" || provider.calls.Load() != 2 {
		t.Fatalf("failed commit recovery: %+v, calls=%d, %v", rows, provider.calls.Load(), err)
	}
	t.Logf("failed commit recovery (lease time simulated): calls=%d; retries=%d; duplicate incidents=%d", provider.calls.Load(), provider.calls.Load()-1, len(rows)-1)
}

// Close and reopen the actual SQLite database, then recover an expired lease.
func TestRecoveryAfterDatabaseReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "restart.db")
	db, err := storesqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	repo := anomalysqlite.New(db)
	a := domain.Anomaly{Type: "high_error_rate", Severity: "high", Message: "rule alert", DetectedAt: time.Now()}
	if err := repo.SaveWithAnalysis(ctx, "p", &a, domain.Snapshot{}, 10); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := repo.ClaimAnalysis(ctx, time.Now().Add(-2*time.Minute), time.Minute); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = storesqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo = anomalysqlite.New(db)
	provider := &countingAnalyst{}
	updates := &recoveryUpdates{events: make(chan domain.Anomaly, 1)}
	worker := NewWorker(repo, provider, nil)
	worker.SetBroadcaster(updates)
	started := time.Now()
	worker.Start()
	defer worker.Stop()
	select {
	case updated := <-updates.events:
		if updated.ID != a.ID || updated.AnalysisStatus != "completed" {
			t.Fatalf("bad recovered update: %+v", updated)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("expired lease not recovered after reopen")
	}
	rows, err := repo.List(ctx, "p", 10)
	if err != nil || len(rows) != 1 || rows[0].ID != a.ID || rows[0].AnalysisStatus != "completed" || provider.calls.Load() != 1 {
		t.Fatalf("restart recovery: %+v, calls=%d, err=%v", rows, provider.calls.Load(), err)
	}
	t.Logf("database reopen recovery: %s; provider calls: %d; duplicate incidents: %d", time.Since(started).Round(time.Millisecond), provider.calls.Load(), len(rows)-1)
}
