package middleware

import (
	"net/http"
	"time"

	"traffic-center/domain"
	"traffic-center/internal/httpx"
)

// RequireAdmin guards mutating routes when ADMIN_EMAILS is configured.
func RequireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identity, ok := domain.IdentityFrom(r.Context())
		if !ok {
			httpx.Error(w, r, domain.ErrUnauthorized)
			return
		}
		if !identity.Admin {
			httpx.Error(w, r, domain.ErrForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Timeout bounds API handlers. It must not wrap the WebSocket route.
func Timeout(duration time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.TimeoutHandler(next, duration, `{"error":"request timed out"}`)
	}
}
