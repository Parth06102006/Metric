package middleware

import (
	"net"
	"net/http"
	"strings"
)

// RealIP rewrites RemoteAddr from the proxy headers when TRUST_PROXY is set.
// The API must only be reachable through the edge proxy for this to be safe.
func RealIP(trust bool) func(http.Handler) http.Handler {
	if !trust {
		return func(next http.Handler) http.Handler { return next }
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if ip := forwardedIP(r); ip != "" {
				r.RemoteAddr = net.JoinHostPort(ip, "0")
			}
			next.ServeHTTP(w, r)
		})
	}
}

func forwardedIP(r *http.Request) string {
	if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
		first := strings.TrimSpace(strings.Split(forwarded, ",")[0])
		if net.ParseIP(first) != nil {
			return first
		}
	}
	if realIP := strings.TrimSpace(r.Header.Get("X-Real-IP")); net.ParseIP(realIP) != nil {
		return realIP
	}
	return ""
}
