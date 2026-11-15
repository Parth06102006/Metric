package domain

import (
	"math"
	"strings"
	"testing"
	"time"
)

func TestLogRecordRejectsUnsafeBounds(t *testing.T) {
	base := LogRecord{Ts: time.Now(), Endpoint: "/", Method: "GET", Status: 200, IP: "192.0.2.1"}
	for _, tc := range []struct {
		name   string
		modify func(*LogRecord)
	}{
		{"endpoint bytes", func(r *LogRecord) { r.Endpoint = strings.Repeat("a", 2049) }},
		{"method bytes", func(r *LogRecord) { r.Method = strings.Repeat("G", 17) }},
		{"nan", func(r *LogRecord) { r.LatencyMs = math.NaN() }},
		{"infinity", func(r *LogRecord) { r.LatencyMs = math.Inf(1) }},
		{"too much latency", func(r *LogRecord) { r.LatencyMs = 86400001 }},
		{"too much payload", func(r *LogRecord) { r.PayloadBytes = 1<<40 + 1 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := base
			tc.modify(&r)
			if r.Validate() == nil {
				t.Fatal("invalid record accepted")
			}
		})
	}
}
