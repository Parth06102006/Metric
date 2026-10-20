package middleware

import (
	"net/http"

	"traffic-center/internal/reqid"
)

// RequestID trusts an inbound X-Request-ID (the edge proxy may set one) or
// generates one, then exposes it to handlers and the response.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if id == "" || len(id) > 128 {
			id = reqid.New()
		}

		w.Header().Set("X-Request-ID", id)
		next.ServeHTTP(w, r.WithContext(reqid.With(r.Context(), id)))
	})
}
