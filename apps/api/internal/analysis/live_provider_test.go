package analysis

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	anomalysqlite "traffic-center/anomaly/repository/sqlite"
	"traffic-center/domain"
	"traffic-center/internal/jev"
	storesqlite "traffic-center/internal/sqlite"
	metricssqlite "traffic-center/metrics/repository/sqlite"
)

type observedLiveAnalyst struct {
	client  *jev.Client
	calls   int
	elapsed time.Duration
	result  jev.Result
	err     error
}

func (a *observedLiveAnalyst) Diagnose(ctx context.Context, state jev.State) (jev.Result, error) {
	a.calls++
	start := time.Now()
	a.result, a.err = a.client.Diagnose(ctx, state)
	a.elapsed = time.Since(start)
	return a.result, a.err
}

// Opt-in only: this test sends one synthetic, aggregate-only, potentially
// billable request to the real provider. It never prints credentials.
func TestLiveJevIncidentAnalysis(t *testing.T) {
	if os.Getenv("JEV_LIVE_TEST") != "1" {
		t.Skip("set JEV_LIVE_TEST=1 and OPENROUTER_API_KEY to run one billable live provider request")
	}
	key := os.Getenv("OPENROUTER_API_KEY")
	if key == "" {
		t.Fatal("OPENROUTER_API_KEY is required for the live test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	db, err := storesqlite.Open(filepath.Join(t.TempDir(), "live.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	snapshots := metricssqlite.New(db)
	now := time.Now().UTC().Truncate(time.Millisecond)
	for i := 0; i < 13; i++ {
		if err := snapshots.Save(ctx, "synthetic", domain.Snapshot{Ts: now.Add(-time.Duration(30-i) * time.Minute), WindowS: 60, RPS: 2, P95: 100, ErrorRate: 1}); err != nil {
			t.Fatal(err)
		}
	}
	count := 40
	snapshot := domain.Snapshot{Ts: now, WindowS: 60, SampleCount: &count, RPS: 5, P50: 100, P95: 250, P99: 300, ErrorRate: 30, UniqueIPs: 10, TopIPRPS: 0.2,
		EndpointSummary: &domain.EndpointEvidence{EndpointCount: 3, EndpointsWithErrors: 1, MostErrors: 12, MostErrorsSamples: 20, SlowestP95: 250, SlowestP95Samples: 20}}
	repo := anomalysqlite.New(db)
	anomaly := domain.Anomaly{Type: "high_error_rate", Severity: "critical", Message: "synthetic error rate alert", Value: 30, Threshold: 5, Analysis: "Rule-based alert.", DetectedAt: now}
	if err := repo.SaveWithAnalysis(ctx, "synthetic", &anomaly, snapshot, 10); err != nil {
		t.Fatal(err)
	}
	job, err := repo.ClaimAnalysis(ctx, time.Now(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	model := os.Getenv("JEV_MODEL")
	if model == "" {
		model = "typesafe/jev-1.14"
	} // Match config.Load's application default.
	provider := &observedLiveAnalyst{client: jev.New(key, model)}
	NewWorker(repo, provider, snapshots).process(ctx, job)
	if provider.calls != 1 {
		t.Fatalf("provider calls = %d, want 1", provider.calls)
	}
	if provider.err != nil {
		t.Fatalf("live provider request failed after %s: %v", provider.elapsed.Round(time.Millisecond), provider.err)
	}
	rows, err := repo.List(ctx, "synthetic", 10)
	if err != nil || len(rows) != 1 || rows[0].ID != anomaly.ID || rows[0].AnalysisStatus != "completed" || rows[0].Diagnosis == nil {
		t.Fatalf("analysis was not persisted: rows=%+v err=%v", rows, err)
	}
	if rows[0].Diagnosis.Evidence == nil || rows[0].Diagnosis.Evidence.Baseline == nil {
		t.Fatal("baseline evidence missing from stored diagnosis")
	}
	t.Logf("live provider: model=%s elapsed=%s input_tokens=%d calls=%d duplicates=%d evidence_sufficient=%t", provider.result.Model, provider.elapsed.Round(time.Millisecond), provider.result.InputTokens, provider.calls, len(rows)-1, rows[0].Diagnosis.EvidenceSufficient)
}
