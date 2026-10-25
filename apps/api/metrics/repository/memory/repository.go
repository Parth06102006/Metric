package memory

import (
	"context"
	"sort"
	"sync"
	"time"

	"traffic-center/domain"
)

// Repository keeps the rolling window of request logs in memory.
// It is the production implementation of the live record store; SQLite only
// persists computed snapshots, not raw requests.
type Repository struct {
	mu       sync.Mutex
	window   time.Duration
	projects map[string][]domain.LogRecord
}

// New creates a record repository with the given rolling window.
func New(window time.Duration) *Repository {
	if window <= 0 {
		window = 60 * time.Second
	}
	return &Repository{
		window:   window,
		projects: make(map[string][]domain.LogRecord),
	}
}

func (r *Repository) Append(_ context.Context, projectID string, records []domain.LogRecord) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.projects[projectID] = prune(r.projects[projectID], time.Now().Add(-r.window))
	r.projects[projectID] = append(r.projects[projectID], records...)
	return nil
}

func (r *Repository) Recent(_ context.Context, projectID string, since time.Time) ([]domain.LogRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	records := prune(r.projects[projectID], time.Now().Add(-r.window))
	r.projects[projectID] = records

	result := make([]domain.LogRecord, 0, len(records))
	for _, record := range records {
		if !record.Ts.Before(since) {
			result = append(result, record)
		}
	}
	return result, nil
}

func (r *Repository) ActiveProjects(_ context.Context) []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	cutoff := time.Now().Add(-r.window)
	ids := make([]string, 0, len(r.projects))
	for projectID, records := range r.projects {
		active := prune(records, cutoff)
		if len(active) == 0 {
			delete(r.projects, projectID)
			continue
		}
		ids = append(ids, projectID)
	}
	sort.Strings(ids)
	return ids
}

// Delete drops a project's live window so the worker stops computing
// snapshots for it.
func (r *Repository) Delete(_ context.Context, projectID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	delete(r.projects, projectID)
	return nil
}

func prune(records []domain.LogRecord, cutoff time.Time) []domain.LogRecord {
	active := records[:0]
	for _, record := range records {
		if !record.Ts.Before(cutoff) {
			active = append(active, record)
		}
	}
	return active
}
