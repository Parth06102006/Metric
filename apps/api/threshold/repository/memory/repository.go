package memory

import (
	"context"
	"sync"

	"traffic-center/domain"
)

// Repository is an in-memory ThresholdRepository for usecase tests.
type Repository struct {
	mu         sync.RWMutex
	thresholds map[string]domain.Threshold
}

// New creates an empty repository.
func New() *Repository {
	return &Repository{thresholds: make(map[string]domain.Threshold)}
}

func (r *Repository) Get(_ context.Context, projectID string) (domain.Threshold, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	thresholds, ok := r.thresholds[projectID]
	if !ok {
		return domain.Threshold{}, domain.ErrNotFound
	}
	return thresholds, nil
}

func (r *Repository) Upsert(_ context.Context, projectID string, thresholds domain.Threshold) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.thresholds[projectID] = thresholds
	return nil
}
