// Package jev implements an incident classifier using Jev via OpenRouter Decisions.
package jev

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"

	"traffic-center/domain"
)

const defaultBaseURL = "https://openrouter.ai/api/alpha/decisions"
const maxResponseBytes = 1 << 20

// State contains only the aggregate incident facts sent to the provider.
type State = domain.IncidentEvidence
type Baseline = domain.BaselineEvidence

type Result struct {
	Pattern, Direction, Model                                                               string
	Urgency, PatternConfidence, DirectionConfidence, UrgencyConfidence, EvidenceProbability float64
	InputTokens                                                                             int
}

type Client struct {
	apiKey, model, baseURL string
	httpClient             *http.Client
	requestTimeout         time.Duration
}

// New constructs a client; callers decide whether an empty API key disables it.
func New(apiKey, model string) *Client {
	if model == "" {
		model = "typesafe/jev-1.14"
	}
	return &Client{apiKey: apiKey, model: model, baseURL: defaultBaseURL, httpClient: &http.Client{}, requestTimeout: 4 * time.Second}
}

type question struct {
	Type         string `json:"type"`
	Instructions string `json:"instructions"`
	Criteria     any    `json:"criteria,omitempty"`
}

func questions() map[string]question {
	return map[string]question{
		"pattern": {"choice", "Which pattern best describes the incident based on the observed metrics?", map[string]string{
			"traffic_surge": "Traffic has risen sharply relative to normal.", "error_burst": "Errors dominate the incident.", "latency_degradation": "Response latency is degraded.", "mixed": "Multiple distinct signals are degraded.", "unclear": "The pattern is not sufficiently clear."}},
		"direction": {"choice", "Which area should be investigated first?", map[string]string{
			"traffic_sources": "Investigate traffic sources or concentration.", "failing_endpoints": "Investigate endpoints producing failures.", "latency": "Investigate latency and slow requests.", "more_evidence": "Collect more evidence before choosing an area."}},
		"urgency":  {"score", "How urgently does this incident require investigation?", []string{"Low urgency; routine investigation can wait.", "Moderate urgency; investigate soon.", "High urgency; investigate immediately."}},
		"evidence": {"noul", "Are the supplied aggregate facts sufficient to identify the incident pattern and investigation direction?", nil},
	}
}

type answer struct {
	Type          string                     `json:"type"`
	Choice        string                     `json:"choice"`
	Confidence    *float64                   `json:"confidence"`
	Score         *float64                   `json:"score"`
	Noul          *float64                   `json:"noul"`
	Probabilities map[string]float64         `json:"probabilities"`
	Legend        map[string]json.RawMessage `json:"legend"`
}
type response struct {
	Model   string            `json:"model"`
	Answers map[string]answer `json:"answers"`
	Usage   *struct {
		InputTokens  *int `json:"input_tokens"`
		OutputTokens *int `json:"output_tokens"`
	} `json:"usage"`
}

// Diagnose sends all four independent questions in one request.
func (c *Client) Diagnose(ctx context.Context, state State) (Result, error) {
	body, err := json.Marshal(struct {
		Model     string              `json:"model"`
		State     State               `json:"state"`
		Questions map[string]question `json:"questions"`
	}{c.model, state, questions()})
	if err != nil {
		return Result{}, fmt.Errorf("jev: invalid state")
	}
	ctx, cancel := context.WithTimeout(ctx, c.requestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL, bytes.NewReader(body))
	if err != nil {
		return Result{}, fmt.Errorf("jev: could not construct request")
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	res, err := c.httpClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return Result{}, fmt.Errorf("jev: request failed: %w", ctx.Err())
		}
		return Result{}, fmt.Errorf("jev: request failed")
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return Result{}, fmt.Errorf("jev: provider returned status %d", res.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, maxResponseBytes+1))
	if err != nil {
		return Result{}, fmt.Errorf("jev: response read failed")
	}
	if len(data) > maxResponseBytes {
		return Result{}, fmt.Errorf("jev: response too large")
	}
	var parsed response
	if err := json.Unmarshal(data, &parsed); err != nil {
		return Result{}, fmt.Errorf("jev: invalid response JSON")
	}
	return parseResponse(parsed)
}

func parseResponse(r response) (Result, error) {
	p, d, u, e := r.Answers["pattern"], r.Answers["direction"], r.Answers["urgency"], r.Answers["evidence"]
	if strings.TrimSpace(r.Model) == "" || r.Usage == nil || r.Usage.InputTokens == nil || r.Usage.OutputTokens == nil || *r.Usage.InputTokens < 0 || *r.Usage.OutputTokens < 0 || p.Type != "choice" || d.Type != "choice" || u.Type != "score" || e.Type != "noul" || p.Confidence == nil || d.Confidence == nil || u.Confidence == nil || u.Score == nil || e.Noul == nil {
		return Result{}, fmt.Errorf("jev: incomplete response")
	}
	patterns := map[string]bool{"traffic_surge": true, "error_burst": true, "latency_degradation": true, "mixed": true, "unclear": true}
	directions := map[string]bool{"traffic_sources": true, "failing_endpoints": true, "latency": true, "more_evidence": true}
	valid := func(x float64, max float64) bool { return !math.IsNaN(x) && !math.IsInf(x, 0) && x >= 0 && x <= max }
	if !patterns[p.Choice] || !directions[d.Choice] || !valid(*p.Confidence, 1) || !valid(*d.Confidence, 1) || !valid(*u.Confidence, 1) || !valid(*u.Score, 2) || !valid(*e.Noul, 1) {
		return Result{}, fmt.Errorf("jev: invalid answer")
	}
	return Result{Pattern: p.Choice, Direction: d.Choice, Model: r.Model, Urgency: *u.Score, PatternConfidence: *p.Confidence, DirectionConfidence: *d.Confidence, UrgencyConfidence: *u.Confidence, EvidenceProbability: *e.Noul, InputTokens: *r.Usage.InputTokens}, nil
}
