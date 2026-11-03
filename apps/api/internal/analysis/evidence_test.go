package analysis

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	anomalysqlite "traffic-center/anomaly/repository/sqlite"
	"traffic-center/domain"
	"traffic-center/internal/jev"
	storesqlite "traffic-center/internal/sqlite"
	metricssqlite "traffic-center/metrics/repository/sqlite"
)

type evidenceAnalyst struct{ state jev.State }

func (a *evidenceAnalyst) Diagnose(_ context.Context, state jev.State) (jev.Result, error) {
	a.state = state
	return jev.Result{Pattern: "error_burst", Direction: "failing_endpoints", EvidenceProbability: 0.9, Model: "fake"}, nil
}

func TestAnalysisPreservesAnonymousEvidenceAndUsesNonOverlappingBaseline(t *testing.T) {
	ctx := context.Background()
	db, err := storesqlite.Open(filepath.Join(t.TempDir(), "evidence.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	snapshots := metricssqlite.New(db)
	observed := time.Now().UTC().Truncate(time.Millisecond)
	for i := 0; i < 13; i++ {
		s := domain.Snapshot{Ts: observed.Add(-time.Duration(30-i) * time.Minute), WindowS: 120, RPS: 2, P95: 100, ErrorRate: 1}
		if err := snapshots.Save(ctx, "p", s); err != nil {
			t.Fatal(err)
		}
	}
	// This overlaps the incident's two-minute window and must not enter the baseline.
	if err := snapshots.Save(ctx, "p", domain.Snapshot{Ts: observed.Add(-time.Minute), RPS: 999, P95: 999}); err != nil {
		t.Fatal(err)
	}
	records := make([]domain.LogRecord, 20)
	for i := range records {
		records[i] = domain.LogRecord{Endpoint: "/secret", IP: "192.0.2.1", Status: 500, LatencyMs: 200}
	}
	incident := domain.ComputeAt(records, 120, observed)
	repo := anomalysqlite.New(db)
	alert := domain.Anomaly{Type: "high_error_rate", Severity: "high", Message: "errors", DetectedAt: observed}
	if err := repo.SaveWithAnalysis(ctx, "p", &alert, incident, 10); err != nil {
		t.Fatal(err)
	}
	job, err := repo.ClaimAnalysis(ctx, time.Now(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	provider := &evidenceAnalyst{}
	NewWorker(repo, provider, snapshots).process(ctx, job)
	if provider.state.Baseline == nil || provider.state.Baseline.Samples != 13 || provider.state.Baseline.End.After(observed.Add(-2*time.Minute)) {
		t.Fatalf("bad baseline: %+v", provider.state.Baseline)
	}
	rows, err := repo.List(ctx, "p", 1)
	if err != nil || len(rows) != 1 {
		t.Fatalf("list: %+v %v", rows, err)
	}
	d := rows[0].Diagnosis
	if d == nil || !d.EvidenceSufficient || d.Evidence == nil || d.Evidence.RequestCount == nil || *d.Evidence.RequestCount != 20 || d.Evidence.Endpoints == nil || d.Evidence.Endpoints.MostErrors != 20 {
		t.Fatalf("missing evidence: %+v", d)
	}
	encoded, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "/secret") || strings.Contains(string(encoded), "192.0.2.1") {
		t.Fatal("stored evidence leaked raw path or IP")
	}
}

func TestSuggestionsWithheldForInsufficientEvidence(t *testing.T) {
	for _, tc := range []struct {
		name   string
		count  int
		errors int
	}{{"too few requests", 19, 1}, {"no endpoint failures", 20, 0}} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			db, err := storesqlite.Open(filepath.Join(t.TempDir(), "evidence.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			snapshots := metricssqlite.New(db)
			now := time.Now().UTC().Truncate(time.Millisecond)
			for i := 0; i < 12; i++ {
				if err := snapshots.Save(ctx, "p", domain.Snapshot{Ts: now.Add(-time.Duration(30-i) * time.Minute), WindowS: 60}); err != nil {
					t.Fatal(err)
				}
			}
			snapshot := domain.Snapshot{Ts: now, WindowS: 60, SampleCount: &tc.count, EndpointSummary: &domain.EndpointEvidence{MostErrors: tc.errors}}
			repo := anomalysqlite.New(db)
			a := domain.Anomaly{Type: "x", Severity: "high", Message: "rule", DetectedAt: now}
			if err := repo.SaveWithAnalysis(ctx, "p", &a, snapshot, 10); err != nil {
				t.Fatal(err)
			}
			job, err := repo.ClaimAnalysis(ctx, now.Add(time.Second), time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			NewWorker(repo, &evidenceAnalyst{}, snapshots).process(ctx, job)
			rows, err := repo.List(ctx, "p", 1)
			if err != nil || len(rows) != 1 || rows[0].Diagnosis == nil || rows[0].Diagnosis.EvidenceSufficient || !strings.Contains(rows[0].Analysis, "insufficient evidence") {
				t.Fatalf("unexpected suggestion: %+v, %v", rows, err)
			}
		})
	}
}

func TestBaselineRequiresHistory(t *testing.T) {
	ctx := context.Background()
	db, err := storesqlite.Open(filepath.Join(t.TempDir(), "baseline.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	snapshots := metricssqlite.New(db)
	now := time.Now()
	if err := snapshots.Save(ctx, "p", domain.Snapshot{Ts: now.Add(-10 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if got := NewWorker(nil, nil, snapshots).baseline(ctx, "p", now, 60); got != nil {
		t.Fatalf("insufficient history produced baseline: %+v", got)
	}
}
