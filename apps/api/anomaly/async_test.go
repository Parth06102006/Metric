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

type asyncRepo struct {
	*anomalymemory.Repository
	queued int
}

func (r *asyncRepo) SaveWithAnalysis(ctx context.Context, p string, a *domain.Anomaly, s domain.Snapshot, max int) error {
	r.queued++
	a.AnalysisStatus = "pending"
	return r.Save(ctx, p, a)
}
func TestAsyncEvaluationDoesNotCallExplainer(t *testing.T) {
	repo := &asyncRepo{Repository: anomalymemory.New()}
	thresholds := thresholdmemory.New()
	fake := &fakeExplainer{}
	if err := thresholds.Upsert(context.Background(), "p", domain.Threshold{RPSMax: 1}); err != nil {
		t.Fatal(err)
	}
	usecase := anomaly.NewUsecase(repo, thresholds, fake, nil, time.Minute)
	usecase.EnableAsyncAnalysis(10)
	if err := usecase.Evaluate(context.Background(), "p", domain.Snapshot{RPS: 10}); err != nil {
		t.Fatal(err)
	}
	stored, err := repo.List(context.Background(), "p", 10)
	if err != nil {
		t.Fatal(err)
	}
	if fake.calls != 0 || repo.queued != 1 || len(stored) != 1 || stored[0].AnalysisStatus != "pending" || stored[0].Analysis == "" {
		t.Fatalf("calls=%d jobs=%d stored=%+v", fake.calls, repo.queued, stored)
	}
}
