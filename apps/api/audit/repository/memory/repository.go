package memory

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"traffic-center/domain"
)

// Repository is an in-memory AuditRepository for usecase tests.
type Repository struct {
	mu     sync.Mutex
	events []domain.AuditEvent
	nextID int64
}

// New creates an empty repository.
func New() *Repository {
	return &Repository{}
}

// Record appends one event and assigns its ID, mirroring the SQLite adapter.
func (r *Repository) Record(_ context.Context, event domain.AuditEvent) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.nextID++
	event.ID = r.nextID
	r.events = append(r.events, cloneEvent(event))
	return nil
}

// List returns matching events newest first.
func (r *Repository) List(_ context.Context, query domain.AuditQuery) ([]domain.AuditEvent, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	limit := query.Limit
	if limit <= 0 {
		limit = 100
	}

	action := strings.TrimSpace(query.Action)
	projectID := strings.TrimSpace(query.ProjectID)

	matched := make([]domain.AuditEvent, 0, len(r.events))
	for _, event := range r.events {
		if action != "" && event.Action != action {
			continue
		}
		if projectID != "" && event.TargetProjectID != projectID {
			continue
		}
		if !query.Before.IsZero() {
			older := event.Ts.Before(query.Before)
			sameMillisecond := event.Ts.Equal(query.Before) && event.ID < query.BeforeID
			if !older && !sameMillisecond {
				continue
			}
		}
		matched = append(matched, cloneEvent(event))
	}

	sort.SliceStable(matched, func(i, j int) bool {
		if matched[i].Ts.Equal(matched[j].Ts) {
			return matched[i].ID > matched[j].ID
		}
		return matched[i].Ts.After(matched[j].Ts)
	})

	if len(matched) > limit {
		matched = matched[:limit]
	}
	return matched, nil
}

// Prune deletes events older than the cutoff and returns how many were removed.
func (r *Repository) Prune(_ context.Context, before time.Time) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	kept := r.events[:0]
	var pruned int64
	for _, event := range r.events {
		if event.Ts.Before(before) {
			pruned++
			continue
		}
		kept = append(kept, event)
	}
	r.events = kept
	return pruned, nil
}

func cloneEvent(event domain.AuditEvent) domain.AuditEvent {
	if event.Details == nil {
		return event
	}
	details := make(map[string]string, len(event.Details))
	for key, value := range event.Details {
		details[key] = value
	}
	event.Details = details
	return event
}
