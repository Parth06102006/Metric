package memory

import (
	"context"
	"sort"
	"sync"
	"time"

	"traffic-center/domain"
)

// Repository is an in-memory AnomalyRepository for usecase tests.
type Repository struct {
	mu        sync.RWMutex
	anomalies map[string][]domain.Anomaly
	nextID    int64
}

// New creates an empty repository.
func New() *Repository {
	return &Repository{anomalies: make(map[string][]domain.Anomaly)}
}

func (r *Repository) Save(_ context.Context, projectID string, anomaly *domain.Anomaly) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.nextID++
	anomaly.ID = r.nextID
	r.anomalies[projectID] = append(r.anomalies[projectID], *anomaly)
	return nil
}

func (r *Repository) List(_ context.Context, projectID string, limit int) ([]domain.Anomaly, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	stored := r.anomalies[projectID]
	result := make([]domain.Anomaly, len(stored))
	copy(result, stored)

	sort.Slice(result, func(i, j int) bool {
		return result[i].DetectedAt.After(result[j].DetectedAt)
	})
	if limit > 0 && len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

func (r *Repository) HasRecent(_ context.Context, projectID, anomalyType string, since time.Time) (bool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	for _, anomaly := range r.anomalies[projectID] {
		if anomaly.Type == anomalyType && !anomaly.DetectedAt.Before(since) {
			return true, nil
		}
	}
	return false, nil
}
