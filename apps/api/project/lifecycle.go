package project

import (
	"context"
	"strings"

	"traffic-center/domain"
)

// Lifecycle implements API-key rotation and revocation plus project deletion.
// These are dangerous operations; the delivery layer must guard them behind
// admin authorization (the same rule as project creation and threshold
// updates).
type Lifecycle struct {
	projects domain.ProjectRepository
	records  domain.RecordRepository // live window to purge, nil-safe
	events   domain.Broadcaster      // live streams to disconnect, nil-safe
}

// NewLifecycle wires the lifecycle usecase.
func NewLifecycle(projects domain.ProjectRepository, records domain.RecordRepository, events domain.Broadcaster) *Lifecycle {
	return &Lifecycle{projects: projects, records: records, events: events}
}

// RotateKey mints a fresh, cryptographically random ingest key and overwrites
// the stored hash, so the previous key stops working immediately. The
// plaintext is returned exactly once and only its hash is persisted.
func (l *Lifecycle) RotateKey(ctx context.Context, projectID string) (domain.ProjectWithKey, error) {
	if strings.TrimSpace(projectID) == "" {
		return domain.ProjectWithKey{}, domain.Validationf("project id is required")
	}

	existing, err := l.projects.Get(ctx, projectID)
	if err != nil {
		return domain.ProjectWithKey{}, err
	}

	key, err := domain.NewID(32)
	if err != nil {
		return domain.ProjectWithKey{}, err
	}

	if err := l.projects.UpdateKeyHash(ctx, projectID, domain.HashAPIKey(key)); err != nil {
		return domain.ProjectWithKey{}, err
	}

	return domain.ProjectWithKey{Project: existing, APIKey: key}, nil
}

// RevokeKey invalidates the ingest key. Ingest traffic is rejected until a
// new key is rotated.
func (l *Lifecycle) RevokeKey(ctx context.Context, projectID string) error {
	if strings.TrimSpace(projectID) == "" {
		return domain.Validationf("project id is required")
	}
	return l.projects.RevokeKey(ctx, projectID)
}

// DeleteProject removes a project and its persisted data, purges the live
// record window so the worker stops computing snapshots for it, and
// disconnects every live-stream subscriber.
func (l *Lifecycle) DeleteProject(ctx context.Context, projectID string) error {
	if strings.TrimSpace(projectID) == "" {
		return domain.Validationf("project id is required")
	}

	if _, err := l.projects.Get(ctx, projectID); err != nil {
		return err
	}

	if err := l.projects.Delete(ctx, projectID); err != nil {
		return err
	}

	if l.records != nil {
		if err := l.records.Delete(ctx, projectID); err != nil {
			return err
		}
	}
	if l.events != nil {
		l.events.Drop(projectID)
	}
	return nil
}
