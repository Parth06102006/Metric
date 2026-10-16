package anomaly

import (
	"fmt"
	"time"

	"traffic-center/backend/metrics"
)

// Anomaly describes a detected traffic abnormality.
type Anomaly struct {
	Type       string    `json:"type"`
	Severity   string    `json:"severity"`
	Message    string    `json:"message"`
	Value      float64   `json:"value"`
	Threshold  float64   `json:"threshold"`
	DetectedAt time.Time `json:"detected_at"`
}

// Thresholds defines the limits used to detect anomalies.
type Thresholds struct {
	MaxRPS       float64
	MaxErrorRate float64
	MaxP95       float64
	MaxTopIPRPS  float64
}

// Detect checks a metrics snapshot against configured thresholds.
func Detect(snapshot metrics.Snapshot, thresholds Thresholds) []Anomaly {
	var anomalies []Anomaly
	now := time.Now()

	add := func(kind, severity, message string, value, threshold float64) {
		anomalies = append(anomalies, Anomaly{
			Type:       kind,
			Severity:   severity,
			Message:    message,
			Value:      value,
			Threshold:  threshold,
			DetectedAt: now,
		})
	}

	if thresholds.MaxRPS > 0 && snapshot.RPS > thresholds.MaxRPS {
		add("high_traffic", "high",
			fmt.Sprintf("Request rate %.2f RPS exceeds the configured limit.", snapshot.RPS),
			snapshot.RPS, thresholds.MaxRPS)
	}

	if thresholds.MaxErrorRate > 0 && snapshot.ErrorRate > thresholds.MaxErrorRate {
		add("high_error_rate", "critical",
			fmt.Sprintf("Error rate %.2f%% exceeds the configured limit.", snapshot.ErrorRate),
			snapshot.ErrorRate, thresholds.MaxErrorRate)
	}

	if thresholds.MaxP95 > 0 && snapshot.P95 > thresholds.MaxP95 {
		add("high_latency", "high",
			fmt.Sprintf("P95 latency %.2f ms exceeds the configured limit.", snapshot.P95),
			snapshot.P95, thresholds.MaxP95)
	}

	if thresholds.MaxTopIPRPS > 0 && snapshot.TopIPRPS > thresholds.MaxTopIPRPS {
		add("possible_ip_flood", "high",
			fmt.Sprintf("A single IP is generating %.2f requests/sec, above the configured limit.", snapshot.TopIPRPS),
			snapshot.TopIPRPS, thresholds.MaxTopIPRPS)
	}

	return anomalies
}
