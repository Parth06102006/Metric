package domain_test

import (
	"testing"

	"traffic-center/domain"
)

func TestDetect(t *testing.T) {
	snapshot := domain.Snapshot{
		RPS:       150,
		ErrorRate: 12,
		P95:       900,
		TopIPRPS:  40,
	}

	anomalies := domain.Detect(snapshot, domain.Threshold{
		RPSMax:      100,
		ErrorRate:   5,
		P95Ms:       500,
		ReqPerIPMax: 30,
	})

	if len(anomalies) != 4 {
		t.Fatalf("expected 4 anomalies, got %d: %+v", len(anomalies), anomalies)
	}

	types := make(map[string]bool)
	for _, anomaly := range anomalies {
		types[anomaly.Type] = true
		if anomaly.DetectedAt.IsZero() {
			t.Fatalf("anomaly %s has zero timestamp", anomaly.Type)
		}
	}
	for _, want := range []string{"high_traffic", "high_error_rate", "high_latency", "possible_ip_flood"} {
		if !types[want] {
			t.Fatalf("missing anomaly type %q in %+v", want, types)
		}
	}
}

func TestDetectZeroThresholdsDisableRules(t *testing.T) {
	snapshot := domain.Snapshot{RPS: 1000, ErrorRate: 100, P95: 10000, TopIPRPS: 1000}
	if got := domain.Detect(snapshot, domain.Threshold{}); len(got) != 0 {
		t.Fatalf("expected no anomalies with zero thresholds, got %+v", got)
	}
}
