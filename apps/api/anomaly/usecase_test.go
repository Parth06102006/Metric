package anomaly_test

import (
	"context"
	"testing"
	"time"

	"traffic-center/anomaly"
	anomalymemory "traffic-center/anomaly/repository/memory"
	"traffic-center/domain"
	thresholdmemory "traffic-center/threshold/repository/memory"
)

type fakeBroadcaster struct {
	snapshots int
	anomalies []domain.Anomaly
}

func (f *fakeBroadcaster) BroadcastSnapshot(string, domain.Snapshot) { f.snapshots++ }
func (f *fakeBroadcaster) BroadcastAnomaly(_ string, a domain.Anomaly) {
	f.anomalies = append(f.anomalies, a)
}

func (f *fakeBroadcaster) Drop(string) {}

type fakeExplainer struct{ calls int }

func (f *fakeExplainer) Explain(context.Context, string, domain.Anomaly, domain.Snapshot) string {
	f.calls++
	return "explained"
}

func TestEvaluatePersistsDeduplicatesAndBroadcasts(t *testing.T) {
	anomalies := anomalymemory.New()
	thresholds := thresholdmemory.New()
	broadcaster := &fakeBroadcaster{}
	explainer := &fakeExplainer{}

	usecase := anomaly.NewUsecase(anomalies, thresholds, explainer, broadcaster, time.Minute)

	if err := thresholds.Upsert(context.Background(), "p1", domain.Threshold{
		RPSMax: 1, ErrorRate: 1, P95Ms: 10, ReqPerIPMax: 0,
	}); err != nil {
		t.Fatalf("seed thresholds: %v", err)
	}

	snapshot := domain.Snapshot{RPS: 10, ErrorRate: 20, P95: 50, TopIPRPS: 1}

	if err := usecase.Evaluate(context.Background(), "p1", snapshot); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}

	stored, err := anomalies.List(context.Background(), "p1", 10)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(stored) != 3 {
		t.Fatalf("stored = %d anomalies, want 3", len(stored))
	}
	for _, a := range stored {
		if a.Analysis != "explained" {
			t.Fatalf("analysis = %q, want explained", a.Analysis)
		}
		if a.ID == 0 {
			t.Fatal("expected persisted ID")
		}
	}
	if len(broadcaster.anomalies) != 3 {
		t.Fatalf("broadcast = %d, want 3", len(broadcaster.anomalies))
	}

	// Second evaluation inside the dedupe window must not persist or broadcast.
	if err := usecase.Evaluate(context.Background(), "p1", snapshot); err != nil {
		t.Fatalf("second Evaluate: %v", err)
	}
	stored, _ = anomalies.List(context.Background(), "p1", 10)
	if len(stored) != 3 {
		t.Fatalf("after dedupe stored = %d, want 3", len(stored))
	}
	if len(broadcaster.anomalies) != 3 {
		t.Fatalf("after dedupe broadcast = %d, want 3", len(broadcaster.anomalies))
	}
}

func TestEvaluateUsesDefaultsWhenThresholdsMissing(t *testing.T) {
	anomalies := anomalymemory.New()
	usecase := anomaly.NewUsecase(anomalies, thresholdmemory.New(), nil, nil, time.Minute)

	// Defaults: 100 RPS, 5% errors, 1000ms p95, 30 top-IP RPS.
	snapshot := domain.Snapshot{RPS: 1, ErrorRate: 0, P95: 10, TopIPRPS: 1}
	if err := usecase.Evaluate(context.Background(), "p1", snapshot); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}

	stored, _ := anomalies.List(context.Background(), "p1", 10)
	if len(stored) != 0 {
		t.Fatalf("stored = %d, want 0", len(stored))
	}
}
