package anomaly_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"traffic-center/anomaly"
	anomalymemory "traffic-center/anomaly/repository/memory"
	"traffic-center/domain"
	thresholdmemory "traffic-center/threshold/repository/memory"
)

type brokenQueue struct{ *anomalymemory.Repository }

func (r *brokenQueue) SaveWithAnalysis(context.Context, string, *domain.Anomaly, domain.Snapshot, int) error {
	return errors.New("queue unavailable")
}
func TestQueueFailureStillPersistsAlert(t *testing.T) {
	repo := &brokenQueue{anomalymemory.New()}
	thresholds := thresholdmemory.New()
	ctx := context.Background()
	if err := thresholds.Upsert(ctx, "p", domain.Threshold{RPSMax: 1}); err != nil {
		t.Fatal(err)
	}
	uc := anomaly.NewUsecase(repo, thresholds, nil, nil, time.Minute)
	uc.EnableAsyncAnalysis(10)
	if err := uc.Evaluate(ctx, "p", domain.Snapshot{RPS: 10}); err != nil {
		t.Fatal(err)
	}
	rows, err := repo.List(ctx, "p", 10)
	if err != nil || len(rows) != 1 || rows[0].AnalysisStatus != "failed" {
		t.Fatalf("alert missing after queue error: %+v %v", rows, err)
	}
}
