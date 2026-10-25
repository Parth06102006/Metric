package audit

import (
	"context"
	"log/slog"
	"time"

	"traffic-center/domain"
	"traffic-center/internal/reqid"
)

// MaxListSize bounds how many records a single read returns.
const MaxListSize = 1000

// Usecase records security-relevant events. It implements domain.AuditSink.
//
// Reliability policy: a failed audit write never blocks the primary
// operation and is never surfaced to the caller (no internal error leaks).
// Instead the failure, including the full event, is emitted to the structured
// application log at ERROR level, which is a distinct channel from the audit
// store. This is loud, not silent: operators alert on audit-write failures.
type Usecase struct {
	repo   domain.AuditRepository
	logger *slog.Logger
}

// NewUsecase wires the audit usecase.
func NewUsecase(repo domain.AuditRepository, logger *slog.Logger) *Usecase {
	if logger == nil {
		logger = slog.Default()
	}
	return &Usecase{repo: repo, logger: logger}
}

// Record implements domain.AuditSink: it normalizes, redacts, and persists
// one event, returning the storage error. Audit write failures are logged to
// the application log at ERROR level and never surfaced to HTTP callers.
func (u *Usecase) Record(ctx context.Context, event domain.AuditEvent) error {
	event.Ts = event.Ts.UTC()
	if event.Ts.IsZero() {
		event.Ts = time.Now().UTC()
	}
	event.ActorEmail = domain.SanitizeActor(event.ActorEmail)
	event.Details = domain.RedactDetails(event.Details)

	if err := u.repo.Record(ctx, event); err != nil {
		u.logger.Error("audit record write failed",
			"action", event.Action,
			"target_project_id", event.TargetProjectID,
			"outcome", event.Outcome,
			"actor_email", event.ActorEmail,
			"error", err,
		)
		return err
	}
	return nil
}

// Log builds and stores one event. The request context supplies the
// correlation ID. Audit writes never fail a request: see the type comment.
func (u *Usecase) Log(ctx context.Context, actorEmail, action, targetProjectID, outcome string, details map[string]string) {
	event := domain.AuditEvent{
		Ts:              time.Now().UTC(),
		ActorEmail:      actorEmail,
		Action:          action,
		TargetProjectID: targetProjectID,
		Outcome:         outcome,
		RequestID:       reqid.From(ctx),
		Details:         details,
	}

	// The error is deliberately discarded here: Record already logs failures
	// loudly, and the primary operation must not be blocked by audit issues.
	_ = u.Record(ctx, event)
}

// List returns recent audit events, oldest-capped, newest first.
func (u *Usecase) List(ctx context.Context, limit int) ([]domain.AuditEvent, error) {
	if limit <= 0 || limit > MaxListSize {
		limit = 100
	}
	return u.repo.List(ctx, limit)
}
