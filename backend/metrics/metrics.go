package metrics

import (
	"math"
	"sort"
	"time"
)

// LogRecord represents one API request.
type LogRecord struct {
	Ts           time.Time `json:"ts"`
	Endpoint     string    `json:"endpoint"`
	Method       string    `json:"method"`
	Status       int       `json:"status"`
	LatencyMs    float64   `json:"latency_ms"`
	IP           string    `json:"ip"`
	PayloadBytes int       `json:"payload_bytes"`
}

// Snapshot contains metrics calculated over a time window.
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
}

// Compute calculates metrics from records in the supplied window.
// Expired records must be removed by the caller.
func Compute(records []LogRecord, windowSeconds int) Snapshot {
	result := Snapshot{
		Ts:        time.Now(),
		WindowS:   windowSeconds,
		Endpoints: make(map[string]int),
	}

	if windowSeconds <= 0 || len(records) == 0 {
		return result
	}

	ips := make(map[string]int)
	latencies := make([]float64, 0, len(records))

	var errorCount int
	var totalPayloadBytes int64

	for _, record := range records {
		latencies = append(latencies, record.LatencyMs)
		result.Endpoints[record.Endpoint]++
		ips[record.IP]++
		totalPayloadBytes += int64(record.PayloadBytes)

		if record.Status >= 500 {
			errorCount++
		}
	}

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
	return sortedValues[rank-1]
}
