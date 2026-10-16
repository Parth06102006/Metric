package metrics

import (
	"math"
	"testing"
)

func TestCompute(t *testing.T) {
	records := []LogRecord{
		{Status: 200, LatencyMs: 100, IP: "1.1.1.1", Endpoint: "/login", PayloadBytes: 100},
		{Status: 500, LatencyMs: 200, IP: "1.1.1.1", Endpoint: "/login", PayloadBytes: 300},
		{Status: 200, LatencyMs: 300, IP: "2.2.2.2", Endpoint: "/home", PayloadBytes: 200},
	}

	got := Compute(records, 60)

	checks := map[string][2]float64{
		"RPS":             {got.RPS, 0.05},
		"ErrorRate":       {got.ErrorRate, 100.0 / 3},
		"P50":             {got.P50, 200},
		"P95":             {got.P95, 300},
		"P99":             {got.P99, 300},
		"TopIPRPS":        {got.TopIPRPS, 2.0 / 60},
		"AvgPayloadBytes": {got.AvgPayloadBytes, 200},
	}

	for name, values := range checks {
		if math.Abs(values[0]-values[1]) > 0.001 {
			t.Errorf("%s = %v, want %v", name, values[0], values[1])
		}
	}

	if got.UniqueIPs != 2 {
		t.Errorf("UniqueIPs = %d, want 2", got.UniqueIPs)
	}
	if got.Endpoints["/login"] != 2 || got.Endpoints["/home"] != 1 {
		t.Errorf("unexpected endpoint counts: %v", got.Endpoints)
	}
}
