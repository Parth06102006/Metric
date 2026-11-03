package domain

import "time"

// IncidentEvidence contains only aggregate measurements; no raw paths or IPs.
// Optional fields are absent when older queued jobs lack the data.
type IncidentEvidence struct {
	TriggerType   string            `json:"trigger_type"`
	Severity      string            `json:"severity"`
	Value         float64           `json:"value"`
	Threshold     float64           `json:"threshold"`
	ObservedAt    *time.Time        `json:"observed_at,omitempty"`
	WindowSeconds int               `json:"window_seconds"`
	RequestCount  *int              `json:"request_count,omitempty"`
	RPS           float64           `json:"rps"`
	P50           float64           `json:"p50"`
	P95           float64           `json:"p95"`
	P99           float64           `json:"p99"`
	ErrorRate     float64           `json:"error_rate"`
	TopIPRPS      float64           `json:"top_ip_rps"`
	UniqueIPs     int               `json:"unique_ips"`
	Endpoints     *EndpointEvidence `json:"endpoint_summary,omitempty"`
	Baseline      *BaselineEvidence `json:"baseline,omitempty"`
}

// EndpointEvidence summarizes anonymous endpoint statistics, not endpoint names.
type EndpointEvidence struct {
	EndpointCount       int     `json:"endpoint_count"`
	EndpointsWithErrors int     `json:"endpoints_with_errors"`
	MostErrors          int     `json:"most_errors_on_one_endpoint"`
	MostErrorsSamples   int     `json:"most_errors_endpoint_samples"`
	SlowestP95          float64 `json:"slowest_endpoint_p95_ms"`
	SlowestP95Samples   int     `json:"slowest_endpoint_samples"`
}

// BaselineEvidence describes medians of snapshot metrics, not a merged percentile.
type BaselineEvidence struct {
	Samples   int       `json:"samples"`
	Start     time.Time `json:"start"`
	End       time.Time `json:"end"`
	RPS       float64   `json:"rps"`
	P95       float64   `json:"p95"`
	ErrorRate float64   `json:"error_rate"`
}

// Diagnosis is advisory. Its probabilities and scores are not proof of correctness.
type Diagnosis struct {
	Pattern             string            `json:"pattern"`
	Direction           string            `json:"direction"`
	Urgency             float64           `json:"urgency"`
	PatternConfidence   float64           `json:"pattern_confidence"`
	DirectionConfidence float64           `json:"direction_confidence"`
	UrgencyConfidence   float64           `json:"urgency_confidence"`
	EvidenceProbability float64           `json:"evidence_probability"`
	Model               string            `json:"model"`
	InputTokens         int               `json:"input_tokens"`
	EvidenceSufficient  bool              `json:"evidence_sufficient"`
	Evidence            *IncidentEvidence `json:"evidence,omitempty"`
}
