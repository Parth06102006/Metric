package middleware

import (
	"log/slog"
	"net/http"
	"runtime/debug"

	"traffic-center/internal/httpx"
	"traffic-center/internal/reqid"
)

// Recoverer turns a panic into a JSON 500 and logs the stack.
func Recoverer(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				recovered := recover()
				if recovered == nil {
					return
				}
				if recovered == http.ErrAbortHandler {
					panic(recovered)
				}

				logger.Error("panic recovered",
					"error", recovered,
					"path", r.URL.Path,
					"request_id", reqid.From(r.Context()),
					"stack", string(debug.Stack()),
				)

				httpx.JSON(w, http.StatusInternalServerError, httpx.ErrorResponse{
					Error:     "internal server error",
					RequestID: reqid.From(r.Context()),
				})
			}()

			next.ServeHTTP(w, r)
		})
	}
}
