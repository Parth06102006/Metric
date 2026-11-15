package middleware

import (
	"context"
	"net/http"
	"time"
	"traffic-center/domain"
	"traffic-center/internal/httpx"
)

// IngestAdmission bounds concurrent body decodes and gate/DB waiters. No queue:
// overload receives 429 before allocating a body. Deadlines also bound slow uploads.
func IngestAdmission(limit int, timeout time.Duration) func(http.Handler) http.Handler {
	if limit <= 0 {
		limit = 16
	}
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	slots := make(chan struct{}, limit)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			select {
			case slots <- struct{}{}:
				defer func() { <-slots }()
			default:
				httpx.Error(w, r, domain.ErrCapacity)
				return
			}
			controller := http.NewResponseController(w)
			_ = controller.SetReadDeadline(time.Now().Add(timeout))
			defer func() { _ = controller.SetReadDeadline(time.Time{}) }()
			ctx, cancel := context.WithTimeout(r.Context(), timeout)
			defer cancel()
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
