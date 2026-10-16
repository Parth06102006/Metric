package anomaly

import (
	"testing"

	"traffic-center/backend/metrics"
)

func TestDetect_ZeroThresholdsDisabled(t *testing.T) {
	snapshot := metrics.Snapshot{
		RPS:       500.0,
		ErrorRate: 50.0,
		P95:       2000.0,
		TopIPRPS:  100.0,
	}

	thresholds := Thresholds{
		MaxRPS:       0,
		MaxErrorRate: 0,
		MaxP95:       0,
		MaxTopIPRPS:  0,
	}

	anomalies := Detect(snapshot, thresholds)
	if len(anomalies) != 0 {
		t.Fatalf("expected 0 anomalies with zero/disabled thresholds, got %d", len(anomalies))
	}
}

func TestDetect_ThresholdBoundaries(t *testing.T) {
	thresholds := Thresholds{
		MaxRPS:       100.0,
		MaxErrorRate: 5.0,
		MaxP95:       500.0,
		MaxTopIPRPS:  30.0,
	}

	tests := []struct {
		name              string
		snapshot          metrics.Snapshot
		expectedAnomalies int
		expectedType      string
		expectedSeverity  string
	}{
		{
			name: "exact boundary values should not trigger",
			snapshot: metrics.Snapshot{
				RPS:       100.0,
				ErrorRate: 5.0,
				P95:       500.0,
				TopIPRPS:  30.0,
			},
			expectedAnomalies: 0,
		},
		{
			name: "RPS slightly above threshold triggers high_traffic",
			snapshot: metrics.Snapshot{
				RPS: 100.01,
			},
			expectedAnomalies: 1,
			expectedType:      "high_traffic",
			expectedSeverity:  "high",
		},
		{
			name: "ErrorRate slightly above threshold triggers high_error_rate",
			snapshot: metrics.Snapshot{
				ErrorRate: 5.01,
			},
			expectedAnomalies: 1,
			expectedType:      "high_error_rate",
			expectedSeverity:  "critical",
		},
		{
			name: "P95 slightly above threshold triggers high_latency",
			snapshot: metrics.Snapshot{
				P95: 500.01,
			},
			expectedAnomalies: 1,
			expectedType:      "high_latency",
			expectedSeverity:  "high",
		},
		{
			name: "TopIPRPS slightly above threshold triggers possible_ip_flood",
			snapshot: metrics.Snapshot{
				TopIPRPS: 30.01,
			},
			expectedAnomalies: 1,
			expectedType:      "possible_ip_flood",
			expectedSeverity:  "high",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Detect(tc.snapshot, thresholds)
			if len(got) != tc.expectedAnomalies {
				t.Fatalf("expected %d anomalies, got %d: %+v", tc.expectedAnomalies, len(got), got)
			}
			if tc.expectedAnomalies == 1 {
				if got[0].Type != tc.expectedType {
					t.Errorf("expected type %s, got %s", tc.expectedType, got[0].Type)
				}
				if got[0].Severity != tc.expectedSeverity {
					t.Errorf("expected severity %s, got %s", tc.expectedSeverity, got[0].Severity)
				}
				if got[0].DetectedAt.IsZero() {
					t.Errorf("expected non-zero DetectedAt timestamp")
				}
			}
		})
	}
}

func TestDetect_MultipleSimultaneousAnomalies(t *testing.T) {
	thresholds := Thresholds{
		MaxRPS:       100.0,
		MaxErrorRate: 5.0,
		MaxP95:       500.0,
		MaxTopIPRPS:  30.0,
	}

	snapshot := metrics.Snapshot{
		RPS:       150.0,
		ErrorRate: 12.5,
		P95:       850.0,
		TopIPRPS:  45.0,
	}

	got := Detect(snapshot, thresholds)
	if len(got) != 4 {
		t.Fatalf("expected 4 anomalies, got %d", len(got))
	}

	expectedTypes := map[string]bool{
		"high_traffic":      true,
		"high_error_rate":   true,
		"high_latency":      true,
		"possible_ip_flood": true,
	}

	for _, a := range got {
		if !expectedTypes[a.Type] {
			t.Errorf("unexpected anomaly type detected: %s", a.Type)
		}
	}
}
