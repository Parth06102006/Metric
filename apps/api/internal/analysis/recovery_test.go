package analysis

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	anomalysqlite "traffic-center/anomaly/repository/sqlite"
	"traffic-center/domain"
	"traffic-center/internal/jev"
	storesqlite "traffic-center/internal/sqlite"
)

type countingAnalyst struct{ calls atomic.Int64 }

func (a *countingAnalyst) Diagnose(context.Context, jev.State) (jev.Result, error) {
	a.calls.Add(1)
	return jev.Result{Pattern: "unclear", Direction: "more_evidence", Model: "fake"}, nil
}
func TestRestartDoesNotDiscardLeasedJobs(t *testing.T) {
	db, err := storesqlite.Open(filepath.Join(t.TempDir(), "restart.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := anomalysqlite.New(db)
	a := domain.Anomaly{Type: "x", Severity: "high", Message: "x", DetectedAt: time.Now()}
	if err = repo.SaveWithAnalysis(context.Background(), "p", &a, domain.Snapshot{}, 3); err != nil {
		t.Fatal(err)
	}
	if _, err = repo.ClaimAnalysis(context.Background(), time.Now().Add(-2*time.Minute), time.Minute); err != nil {
		t.Fatal(err)
	}
	provider := &countingAnalyst{}
	w := NewWorker(repo, provider, nil)
	w.Start()
	defer w.Stop()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		list, e := repo.List(context.Background(), "p", 10)
		if e == nil && len(list) == 1 && list[0].AnalysisStatus == "completed" {
			if provider.calls.Load() != 1 {
				t.Fatal("wrong provider count")
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("expired lease was not recovered")
}
