package middleware

import (
	"crypto/subtle"
	"net/http"
	"strings"

	"traffic-center/domain"
	"traffic-center/internal/httpx"
)

// IdentityConfig describes how the caller identity is established.
type IdentityConfig struct {
	Mode        string
	EmailHeader string
	UserHeader  string
	ProxySecret string
	DevEmail    string
	AdminEmails map[string]struct{}
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
					httpx.Error(w, r, domain.ErrUnauthorized)
					return
				}
			}

			email := strings.TrimSpace(r.Header.Get(cfg.EmailHeader))
			if email == "" {
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
		return true
	}
	_, ok := admins[strings.ToLower(email)]
	return ok
}
