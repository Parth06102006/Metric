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

// fenceStreams coordinates project access mutations with the WebSocket hub.
// Other broadcaster implementations can omit the fence and remain nil-safe.
func (l *Lifecycle) fenceStreams() func() {
	if fence, ok := l.events.(interface {
		LockStreamAccess()
		UnlockStreamAccess()
	}); ok {
		fence.LockStreamAccess()
		return fence.UnlockStreamAccess
	}
	return func() {}
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

// AssignOwner moves a project to another owner. Admins use it to hand a
// project to the person who should see it and to claim projects created before
// ownership existed. An empty email leaves the project ownerless, so only
// admins can reach it until it is assigned again.
func (l *Lifecycle) AssignOwner(ctx context.Context, projectID, ownerEmail string) (domain.Project, error) {
	if strings.TrimSpace(projectID) == "" {
		return domain.Project{}, domain.Validationf("project id is required")
	}

	ownerEmail = domain.SanitizeActor(ownerEmail)
	if err := domain.ValidateEmail(ownerEmail); err != nil {
		return domain.Project{}, err
	}

	unlock := l.fenceStreams()
	defer unlock()
	existing, err := l.projects.Get(ctx, projectID)
	if err != nil {
		return domain.Project{}, err
	}
	if err := l.projects.SetOwner(ctx, projectID, ownerEmail); err != nil {
		return domain.Project{}, err
	}
	if existing.OwnerEmail != ownerEmail && l.events != nil {
		// Disconnect all project streams; authorized clients can reconnect.
		l.events.Drop(projectID)
	}
	return l.projects.Get(ctx, projectID)
}

// AddViewer grants view-only access to an identity seen through trusted sign-in.
func (l *Lifecycle) AddViewer(ctx context.Context, projectID, email string) error {
	email = domain.SanitizeActor(email)
	if email == "" || domain.ValidateEmail(email) != nil {
		return domain.Validationf("a valid user email is required")
	}
	return l.projects.AddMember(ctx, projectID, email)
}

// RemoveViewer fences stream writes and disconnects clients before returning.
func (l *Lifecycle) RemoveViewer(ctx context.Context, projectID, email string) error {
	email = domain.SanitizeActor(email)
	if email == "" || domain.ValidateEmail(email) != nil {
		return domain.Validationf("a valid user email is required")
	}
	unlock := l.fenceStreams()
	defer unlock()
	if err := l.projects.RemoveMember(ctx, projectID, email); err != nil {
		return err
	}
	if l.events != nil {
		l.events.Drop(projectID)
	}
	return nil
}

// DeleteProject removes a project and its persisted data, purges the live
// record window so the worker stops computing snapshots for it, and
// disconnects every live-stream subscriber.
func (l *Lifecycle) DeleteProject(ctx context.Context, projectID string) error {
	if strings.TrimSpace(projectID) == "" {
		return domain.Validationf("project id is required")
	}

	unlock := l.fenceStreams()
	defer unlock()
	if _, err := l.projects.Get(ctx, projectID); err != nil {
		return err
	}

	if err := l.projects.Delete(ctx, projectID); err != nil {
		return err
	}
	// Revoke streams even if subsequent live-window cleanup fails.
	if l.events != nil {
		l.events.Drop(projectID)
	}
	if l.records != nil {
		if err := l.records.Delete(ctx, projectID); err != nil {
			return err
		}
	}
	return nil
}
