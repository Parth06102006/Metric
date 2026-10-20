package anomaly

import (
	"context"
	"errors"
	"time"

	"traffic-center/domain"
)

// Usecase evaluates a snapshot, deduplicates, explains, persists, and publishes.
type Usecase struct {
	anomalies   domain.AnomalyRepository
	thresholds  domain.ThresholdRepository
	explainer   domain.Explainer
	broadcaster domain.Broadcaster
	dedupe      time.Duration
}

// NewUsecase wires the anomaly usecase. A non-positive dedupe window falls
// back to 60s.
func NewUsecase(
	anomalies domain.AnomalyRepository,
	thresholds domain.ThresholdRepository,
	explainer domain.Explainer,
	broadcaster domain.Broadcaster,
	dedupe time.Duration,
) *Usecase {
	if dedupe <= 0 {
		dedupe = 60 * time.Second
	}
	return &Usecase{
		anomalies:   anomalies,
		thresholds:  thresholds,
		explainer:   explainer,
		broadcaster: broadcaster,
		dedupe:      dedupe,
	}
}

// List returns persisted anomalies, newest first.
func (u *Usecase) List(ctx context.Context, projectID string, limit int) ([]domain.Anomaly, error) {
	return u.anomalies.List(ctx, projectID, limit)
}

// Evaluate runs all rules for one snapshot. Each newly detected anomaly is
// suppressed if the same type fired within the dedupe window.
func (u *Usecase) Evaluate(ctx context.Context, projectID string, snapshot domain.Snapshot) error {
	thresholds, err := u.thresholds.Get(ctx, projectID)
	if errors.Is(err, domain.ErrNotFound) {
		thresholds = domain.DefaultThreshold()
	} else if err != nil {
		return err
	}

	detected := domain.Detect(snapshot, thresholds)
	since := time.Now().Add(-u.dedupe)

	for i := range detected {
		anomaly := &detected[i]

		recent, err := u.anomalies.HasRecent(ctx, projectID, anomaly.Type, since)
		if err != nil {
			return err
		}
		if recent {
			continue
		}

		if u.explainer != nil {
			anomaly.Analysis = u.explainer.Explain(ctx, projectID, *anomaly, snapshot)
		}

		if err := u.anomalies.Save(ctx, projectID, anomaly); err != nil {
			return err
		}

		if u.broadcaster != nil {
			u.broadcaster.BroadcastAnomaly(projectID, *anomaly)
		}
	}

	return nil
}
