package metrics

import (
	"context"
	"time"

	"traffic-center/domain"
)

// Usecase computes the current snapshot and reads historical ones.
type Usecase struct {
	records   domain.RecordRepository
	snapshots domain.SnapshotRepository
	window    time.Duration
}

// NewUsecase wires the metrics usecase. A non-positive window falls back to 60s.
func NewUsecase(records domain.RecordRepository, snapshots domain.SnapshotRepository, window time.Duration) *Usecase {
	if window <= 0 {
		window = 60 * time.Second
	}
	return &Usecase{records: records, snapshots: snapshots, window: window}
}

// Window returns the configured rolling window.
func (u *Usecase) Window() time.Duration {
	return u.window
}

// Current computes a snapshot over the live rolling window.
func (u *Usecase) Current(ctx context.Context, projectID string) (domain.Snapshot, error) {
	records, err := u.records.Recent(ctx, projectID, time.Now().Add(-u.window))
	if err != nil {
		return domain.Snapshot{}, err
	}
	return domain.Compute(records, int(u.window.Seconds())), nil
}

// History returns persisted snapshots in a time range.
func (u *Usecase) History(ctx context.Context, projectID string, from, to time.Time, limit int) ([]domain.Snapshot, error) {
	if limit <= 0 || limit > 5000 {
		limit = 1000
	}
	return u.snapshots.Range(ctx, projectID, from, to, limit)
}
