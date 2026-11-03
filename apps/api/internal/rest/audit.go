package rest

import (
	"context"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"traffic-center/domain"
	"traffic-center/internal/reqid"
)

// actor returns the caller email from the trusted identity context.
func actor(r *http.Request) string {
	identity, ok := domain.IdentityFrom(r.Context())
	if !ok {
		return ""
	}
	return domain.SanitizeActor(identity.Email)
}

// logAudit writes one event through the sink. A nil sink is a no-op, matching
// the middleware and the WebSocket handler, so delivery never depends on the
// audit subsystem being wired.
func logAudit(
	sink domain.AuditSink,
	ctx context.Context,
	actorEmail, action, targetProjectID, outcome string,
	details map[string]string,
) {
	if sink == nil {
		return
	}
	_ = sink.Record(ctx, domain.AuditEvent{
		ActorEmail:      domain.SanitizeActor(actorEmail),
		Action:          action,
		TargetProjectID: targetProjectID,
		Outcome:         outcome,
		RequestID:       reqid.From(ctx),
		Details:         details,
	})
}

// auditDenial records one access.denied event when a usecase rejected an
// authenticated caller that targeted a project it does not own. The HTTP
// response still maps to 404, so the audit trail never leaks existence.
func auditDenial(sink domain.AuditSink, r *http.Request, err error) {
	if !errors.Is(err, domain.ErrAccessDenied) {
		return
	}
	logAudit(sink, r.Context(), actor(r), domain.AuditAccessDenied, chi.URLParam(r, "projectID"),
		domain.AuditOutcomeFailure, map[string]string{
			"reason": "project_access_denied",
			"method": r.Method,
			"path":   r.URL.Path,
		})
}
