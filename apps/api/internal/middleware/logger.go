package middleware

import (
	"bufio"
	"log/slog"
	"net"
	"net/http"
	"time"

	"traffic-center/internal/reqid"
)

// statusWriter records the status and body size while preserving streaming and
// WebSocket upgrade support.
type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (w *statusWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(p)
	w.bytes += n
	return n, err
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (w *statusWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func (w *statusWriter) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// Hijack is required by the WebSocket upgrader.
func (w *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, http.ErrNotSupported
	}
	return hijacker.Hijack()
}

// Logger emits one structured line per request.
func Logger(logger *slog.Logger, userHeader string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			writer := &statusWriter{ResponseWriter: w}

			next.ServeHTTP(writer, r)

			status := writer.status
			if status == 0 {
				status = http.StatusOK
			}

			attrs := []any{
				"method", r.Method,
				"path", r.URL.Path,
				"status", status,
				"bytes", writer.bytes,
				"duration_ms", float64(time.Since(start).Microseconds()) / 1000,
				"request_id", reqid.From(r.Context()),
				"remote_ip", remoteIP(r),
			}
			if user := r.Header.Get(userHeader); user != "" {
				attrs = append(attrs, "user", user)
			}

			switch {
			case status >= http.StatusInternalServerError:
				logger.Error("request", attrs...)
			case status >= http.StatusBadRequest:
				logger.Warn("request", attrs...)
			default:
				logger.Info("request", attrs...)
			}
		})
	}
}

func remoteIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
