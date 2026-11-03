package domain_test

import (
	"testing"
	"time"

	"traffic-center/domain"
)

func TestCompute(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	records := []domain.LogRecord{
		{Ts: now, Endpoint: "/a", Method: "GET", Status: 200, LatencyMs: 10, IP: "1.1.1.1", PayloadBytes: 100},
		{Ts: now, Endpoint: "/a", Method: "GET", Status: 200, LatencyMs: 20, IP: "1.1.1.1", PayloadBytes: 200},
		{Ts: now, Endpoint: "/b", Method: "POST", Status: 500, LatencyMs: 30, IP: "2.2.2.2", PayloadBytes: 300},
	}

	snapshot := domain.ComputeAt(records, 60, now)

	if snapshot.WindowS != 60 {
		t.Fatalf("WindowS = %d, want 60", snapshot.WindowS)
	}
	if snapshot.RPS != 0.05 {
		t.Fatalf("RPS = %f, want 0.05", snapshot.RPS)
	}
	if snapshot.P50 != 20 || snapshot.P95 != 30 || snapshot.P99 != 30 {
		t.Fatalf("percentiles = %f/%f/%f, want 20/30/30", snapshot.P50, snapshot.P95, snapshot.P99)
	}
	if snapshot.ErrorRate < 33.3 || snapshot.ErrorRate > 33.4 {
		t.Fatalf("ErrorRate = %f, want ~33.33", snapshot.ErrorRate)
	}
	if snapshot.UniqueIPs != 2 {
		t.Fatalf("UniqueIPs = %d, want 2", snapshot.UniqueIPs)
	}
	if snapshot.TopIPRPS != 2.0/60 {
		t.Fatalf("TopIPRPS = %f, want %f", snapshot.TopIPRPS, 2.0/60)
	}
	if snapshot.AvgPayloadBytes != 200 {
		t.Fatalf("AvgPayloadBytes = %f, want 200", snapshot.AvgPayloadBytes)
	}
	if snapshot.Endpoints["/a"] != 2 || snapshot.Endpoints["/b"] != 1 {
		t.Fatalf("Endpoints = %v", snapshot.Endpoints)
	}
	if snapshot.SampleCount == nil || *snapshot.SampleCount != 3 {
		t.Fatalf("sample count = %v", snapshot.SampleCount)
	}
	e := snapshot.EndpointSummary
	if e == nil || e.EndpointCount != 2 || e.EndpointsWithErrors != 1 || e.MostErrors != 1 || e.MostErrorsSamples != 1 || e.SlowestP95 != 30 || e.SlowestP95Samples != 1 {
		t.Fatalf("endpoint evidence = %+v", e)
	}
}

func TestComputeEmptyAndZeroWindow(t *testing.T) {
	empty := domain.Compute(nil, 60)
	if empty.RPS != 0 || empty.P95 != 0 || empty.Endpoints == nil {
		t.Fatalf("unexpected empty snapshot: %+v", empty)
	}

	zero := domain.Compute([]domain.LogRecord{{Ts: time.Now(), LatencyMs: 10}}, 0)
	if zero.RPS != 0 {
		t.Fatalf("RPS with zero window = %f, want 0", zero.RPS)
	}
}

func TestLogRecordValidate(t *testing.T) {
	valid := domain.LogRecord{
		Ts: time.Now(), Endpoint: "/x", Method: "GET", Status: 200, LatencyMs: 1, IP: "10.0.0.1",
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid record rejected: %v", err)
	}

	cases := map[string]domain.LogRecord{
		"missing timestamp": {Endpoint: "/x", Method: "GET", Status: 200, IP: "10.0.0.1"},
		"missing endpoint":  {Ts: time.Now(), Method: "GET", Status: 200, IP: "10.0.0.1"},
		"bad status":        {Ts: time.Now(), Endpoint: "/x", Method: "GET", Status: 99, IP: "10.0.0.1"},
		"bad ip":            {Ts: time.Now(), Endpoint: "/x", Method: "GET", Status: 200, IP: "nope"},
	}
	for name, record := range cases {
		if err := record.Validate(); err == nil {
			t.Fatalf("%s: expected validation error", name)
		}
	}
}
