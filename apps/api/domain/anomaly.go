package domain

import (
	"fmt"
	"time"
)

// Severity levels used by the detector and the UI.
const (
	SeverityHigh     = "high"
	SeverityCritical = "critical"
)

// Anomaly is a detected traffic abnormality.
type Anomaly struct {
	ID             int64      `json:"id,omitempty"`
	Type           string     `json:"type"`
	Severity       string     `json:"severity"`
	Message        string     `json:"message"`
	Value          float64    `json:"value"`
	Threshold      float64    `json:"threshold"`
	Analysis       string     `json:"analysis,omitempty"`
	AnalysisStatus string     `json:"analysis_status,omitempty"`
	Diagnosis      *Diagnosis `json:"diagnosis,omitempty"`
	DetectedAt     time.Time  `json:"detected_at"`
}

// Detect checks a snapshot against configured thresholds.
// A threshold value of zero disables that rule.
func Detect(snapshot Snapshot, thresholds Threshold) []Anomaly {
	var anomalies []Anomaly
	now := time.Now().UTC()

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

	if thresholds.RPSMax > 0 && snapshot.RPS > thresholds.RPSMax {
		add("high_traffic", SeverityHigh,
			fmt.Sprintf("Request rate %.2f RPS exceeds the configured limit.", snapshot.RPS),
			snapshot.RPS, thresholds.RPSMax)
	}

	if thresholds.ErrorRate > 0 && snapshot.ErrorRate > thresholds.ErrorRate {
		add("high_error_rate", SeverityCritical,
			fmt.Sprintf("Error rate %.2f%% exceeds the configured limit.", snapshot.ErrorRate),
			snapshot.ErrorRate, thresholds.ErrorRate)
	}

	if thresholds.P95Ms > 0 && snapshot.P95 > thresholds.P95Ms {
		add("high_latency", SeverityHigh,
			fmt.Sprintf("P95 latency %.2f ms exceeds the configured limit.", snapshot.P95),
			snapshot.P95, thresholds.P95Ms)
	}

	if thresholds.ReqPerIPMax > 0 && snapshot.TopIPRPS > thresholds.ReqPerIPMax {
		add("possible_ip_flood", SeverityHigh,
			fmt.Sprintf("A single IP is generating %.2f requests/sec, above the configured limit.", snapshot.TopIPRPS),
			snapshot.TopIPRPS, thresholds.ReqPerIPMax)
	}

	return anomalies
}
