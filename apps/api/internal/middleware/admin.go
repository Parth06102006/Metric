package middleware

import (
	"net/http"
	"time"

	"traffic-center/domain"
	"traffic-center/internal/httpx"
	"traffic-center/internal/reqid"
)

// RequireAdmin guards mutating routes when ADMIN_EMAILS is configured.
// Denied attempts are audit-logged when a sink is provided.
func RequireAdmin(next http.Handler) http.Handler {
	return RequireAdminWith(nil)(next)
}

// RequireAdminWith behaves like RequireAdmin and records admin.denied events.
func RequireAdminWith(sink domain.AuditSink) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			identity, ok := domain.IdentityFrom(r.Context())
			if !ok {
				recordAdminDenied(sink, r, "", "no_identity")
				httpx.Error(w, r, domain.ErrUnauthorized)
				return
			}
			if !identity.Admin {
				recordAdminDenied(sink, r, identity.Email, "not_admin")
				httpx.Error(w, r, domain.ErrForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// recordAdminDenied stores one failed-authorization event. Only the method
// and path are recorded; never bodies, headers, or credentials.
func recordAdminDenied(sink domain.AuditSink, r *http.Request, actorEmail, reason string) {
	if sink == nil {
		return
	}
	sink.Record(r.Context(), domain.AuditEvent{
		Ts:        time.Now().UTC(),
		ActorEmail: domain.SanitizeActor(actorEmail),
		Action:    domain.AuditAdminDenied,
		Outcome:   domain.AuditOutcomeFailure,
		RequestID: reqid.From(r.Context()),
		Details:   map[string]string{"reason": reason, "method": r.Method, "path": r.URL.Path},
	})
}

// Timeout bounds API handlers. It must not wrap the WebSocket route.
func Timeout(duration time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.TimeoutHandler(next, duration, `{"error":"request timed out"}`)
	}
}
