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

type errorAnalyst struct{ calls atomic.Int64 }

func (a *errorAnalyst) Diagnose(context.Context, jev.State) (jev.Result, error) {
	a.calls.Add(1)
	return jev.Result{}, errors.New("provider unavailable")
}
func TestRepeatedProviderFailureLeavesRuleBasedAlert(t *testing.T) {
	ctx := context.Background()
	db, err := storesqlite.Open(filepath.Join(t.TempDir(), "fail.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := anomalysqlite.New(db)
	a := domain.Anomaly{Type: "x", Severity: "high", Message: "rule alert", Analysis: "rule alert", DetectedAt: time.Now()}
	if err = repo.SaveWithAnalysis(ctx, "p", &a, domain.Snapshot{}, 10); err != nil {
		t.Fatal(err)
	}
	provider := &errorAnalyst{}
	w := NewWorker(repo, provider, nil)
	for i := 0; i < 3; i++ {
		job, e := repo.ClaimAnalysis(ctx, time.Now().Add(time.Minute), time.Minute)
		if e != nil {
			t.Fatal(e)
		}
		w.process(ctx, job)
	}
	rows, err := repo.List(ctx, "p", 10)
	if err != nil || len(rows) != 1 || rows[0].AnalysisStatus != "failed" || rows[0].Analysis == "" || provider.calls.Load() != 3 {
		t.Fatalf("failure did not preserve alert: %+v %v", rows, err)
	}
}
