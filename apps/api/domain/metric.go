package domain

import (
	"math"
	"net"
	"sort"
	"strings"
	"time"
)

// LogRecord is one observed API request.
type LogRecord struct {
	Ts           time.Time `json:"ts"`
	Endpoint     string    `json:"endpoint"`
	Method       string    `json:"method"`
	Status       int       `json:"status"`
	LatencyMs    float64   `json:"latency_ms"`
	IP           string    `json:"ip"`
	PayloadBytes int       `json:"payload_bytes"`
}

// Validate enforces the ingest contract.
func (r LogRecord) Validate() error {
	switch {
	case r.Ts.IsZero():
		return Validationf("log record timestamp is required")
	case strings.TrimSpace(r.Endpoint) == "":
		return Validationf("log record endpoint is required")
	case strings.TrimSpace(r.Method) == "":
		return Validationf("log record method is required")
	case r.Status < 100 || r.Status > 599:
		return Validationf("log record status must be a valid HTTP status")
	case r.LatencyMs < 0:
		return Validationf("log record latency must be non-negative")
	case r.PayloadBytes < 0:
		return Validationf("log record payload size must be non-negative")
	case net.ParseIP(r.IP) == nil:
		return Validationf("log record IP is invalid")
	}
	return nil
}

// Snapshot contains metrics calculated over a rolling window.
type Snapshot struct {
	Ts              time.Time      `json:"ts"`
	WindowS         int            `json:"window_s"`
	RPS             float64        `json:"rps"`
	P50             float64        `json:"p50"`
	P95             float64        `json:"p95"`
	P99             float64        `json:"p99"`
	ErrorRate       float64        `json:"error_rate"`
	UniqueIPs       int            `json:"unique_ips"`
	TopIPRPS        float64        `json:"top_ip_rps"`
	AvgPayloadBytes float64        `json:"avg_payload_bytes"`
	Endpoints       map[string]int `json:"endpoints"`
	// Transient evidence for analysis jobs, not part of the snapshot API or history.
	SampleCount     *int              `json:"-"`
	EndpointSummary *EndpointEvidence `json:"-"`
}

// Compute calculates a snapshot using the current time.
func Compute(records []LogRecord, windowSeconds int) Snapshot {
	return ComputeAt(records, windowSeconds, time.Now())
}

// ComputeAt is the testable form of Compute.
func ComputeAt(records []LogRecord, windowSeconds int, now time.Time) Snapshot {
	result := Snapshot{
		Ts:        now.UTC(),
		WindowS:   windowSeconds,
		Endpoints: make(map[string]int),
	}

	if windowSeconds <= 0 || len(records) == 0 {
		return result
	}

	ips := make(map[string]int, len(records))
	latencies := make([]float64, 0, len(records))

	var errorCount int
	var totalPayloadBytes int64
	type endpointStats struct {
		latencies []float64
		errors    int
	}
	endpointData := make(map[string]*endpointStats)

	for _, record := range records {
		latencies = append(latencies, record.LatencyMs)
		result.Endpoints[record.Endpoint]++
		stats := endpointData[record.Endpoint]
		if stats == nil {
			stats = &endpointStats{}
			endpointData[record.Endpoint] = stats
		}
		stats.latencies = append(stats.latencies, record.LatencyMs)
		if record.Status >= 500 {
			stats.errors++
		}
		ips[record.IP]++
		totalPayloadBytes += int64(record.PayloadBytes)

		if record.Status >= 500 {
			errorCount++
		}
	}

	count := len(records)
	result.SampleCount = &count
	summary := &EndpointEvidence{EndpointCount: len(endpointData)}
	for _, stats := range endpointData {
		if stats.errors > 0 {
			summary.EndpointsWithErrors++
		}
		if stats.errors > summary.MostErrors || (stats.errors == summary.MostErrors && len(stats.latencies) > summary.MostErrorsSamples) {
			summary.MostErrors, summary.MostErrorsSamples = stats.errors, len(stats.latencies)
		}
		sort.Float64s(stats.latencies)
		p95 := percentile(stats.latencies, 0.95)
		if p95 > summary.SlowestP95 || (p95 == summary.SlowestP95 && len(stats.latencies) > summary.SlowestP95Samples) {
			summary.SlowestP95, summary.SlowestP95Samples = p95, len(stats.latencies)
		}
	}
	result.EndpointSummary = summary

	result.RPS = float64(len(records)) / float64(windowSeconds)
	result.ErrorRate = float64(errorCount) * 100 / float64(len(records))
	result.UniqueIPs = len(ips)

	var busiestIPCount int
	for _, count := range ips {
		if count > busiestIPCount {
			busiestIPCount = count
		}
	}
	result.TopIPRPS = float64(busiestIPCount) / float64(windowSeconds)
	result.AvgPayloadBytes = float64(totalPayloadBytes) / float64(len(records))

	sort.Float64s(latencies)
	result.P50 = percentile(latencies, 0.50)
	result.P95 = percentile(latencies, 0.95)
	result.P99 = percentile(latencies, 0.99)

	return result
}

// percentile uses the nearest-rank method on sorted values.
func percentile(sortedValues []float64, p float64) float64 {
	if len(sortedValues) == 0 {
		return 0
	}
	rank := int(math.Ceil(p * float64(len(sortedValues))))
	if rank < 1 {
		rank = 1
	}
	if rank > len(sortedValues) {
		rank = len(sortedValues)
	}
	return sortedValues[rank-1]
}
