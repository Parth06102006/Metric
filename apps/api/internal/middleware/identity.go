package middleware

import (
	"crypto/subtle"
	"net/http"
	"strings"
	"time"

	"traffic-center/domain"
	"traffic-center/internal/httpx"
	"traffic-center/internal/reqid"
)

// IdentityConfig describes how the caller identity is established.
type IdentityConfig struct {
	Mode        string
	EmailHeader string
	UserHeader  string
	ProxySecret string
	DevEmail    string
	AdminEmails map[string]struct{}
	// Audit, when set, receives one auth.denied record per failed
	// authentication. Failures never block or alter the response.
	Audit domain.AuditSink
}

// Identity resolves the caller from trusted proxy headers.
//
//	dev   -> inject DEV_USER_EMAIL (never allowed in production)
//	proxy -> require the proxy headers and optional shared secret
func Identity(cfg IdentityConfig) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if cfg.Mode == "dev" {
				next.ServeHTTP(w, r.WithContext(domain.WithIdentity(r.Context(), domain.Identity{
					Email: cfg.DevEmail,
					User:  "dev",
					Admin: true,
				})))
				return
			}

			if cfg.ProxySecret != "" {
				provided := r.Header.Get("X-Proxy-Secret")
				if subtle.ConstantTimeCompare([]byte(provided), []byte(cfg.ProxySecret)) != 1 {
					recordAuthDenied(cfg.Audit, r, "", "proxy_secret_mismatch")
					httpx.Error(w, r, domain.ErrUnauthorized)
					return
				}
			}

			email := strings.TrimSpace(r.Header.Get(cfg.EmailHeader))
			if email == "" {
				recordAuthDenied(cfg.Audit, r, "", "missing_identity_header")
				httpx.Error(w, r, domain.ErrUnauthorized)
				return
			}

			identity := domain.Identity{
				Email: email,
				User:  strings.TrimSpace(r.Header.Get(cfg.UserHeader)),
				Admin: isAdmin(cfg.AdminEmails, email),
			}
			next.ServeHTTP(w, r.WithContext(domain.WithIdentity(r.Context(), identity)))
		})
	}
}

func isAdmin(admins map[string]struct{}, email string) bool {
	if len(admins) == 0 {
		return false
	}
	_, ok := admins[strings.ToLower(email)]
	return ok
}

// recordAuthDenied stores one failed-authentication event. Audit failures are
// logged, never surfaced to the caller, and never block the denial.
func recordAuthDenied(sink domain.AuditSink, r *http.Request, actorEmail, reason string) {
	if sink == nil {
		return
	}
	// The path alone is recorded: no query strings, headers, or bodies.
	_ = sink.Record(r.Context(), domain.AuditEvent{
		Ts:         time.Now().UTC(),
		ActorEmail: domain.SanitizeActor(actorEmail),
		Action:     domain.AuditAuthDenied,
		Outcome:    domain.AuditOutcomeFailure,
		RequestID:  reqid.From(r.Context()),
		Details:    map[string]string{"reason": reason, "path": r.URL.Path},
	})
}
