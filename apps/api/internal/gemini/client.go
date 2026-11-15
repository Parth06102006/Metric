// Package gemini implements the Gemini Decisions API advisory analyst.
package gemini

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"time"

	"traffic-center/domain"
)

const DefaultModel = "gemini-decision-1"
const defaultBaseURL = "https://generativelanguage.googleapis.com/v1beta/decisions"
const maxResponseBytes = 1 << 20

type Client struct {
	apiKey, model, baseURL string
	httpClient             *http.Client
	requestTimeout         time.Duration
}

// New constructs the Decisions API client. A missing key disables the worker at wiring time.
func New(apiKey, model string) *Client {
	if model == "" {
		model = DefaultModel
	}
	return &Client{apiKey: apiKey, model: model, baseURL: defaultBaseURL, requestTimeout: 10 * time.Second,
		httpClient: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
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
	Type       string   `json:"type"`
	Choice     string   `json:"choice"`
	Confidence *float64 `json:"confidence"`
	Score      *float64 `json:"score"`
	Noul       *float64 `json:"noul"`
}
type response struct {
	Answers map[string]answer `json:"answers"`
	Usage   *struct {
		TotalInputTokens  *int `json:"total_input_tokens"`
		TotalOutputTokens *int `json:"total_output_tokens"`
	} `json:"usage"`
}

// Diagnose sends only anonymous aggregate data, serialized as text per Google
// Decisions API. Provider errors deliberately omit bodies, keys, and inputs.
func (c *Client) Diagnose(ctx context.Context, state domain.IncidentEvidence) (domain.AnalysisResult, error) {
	evidence, err := json.Marshal(state)
	if err != nil {
		return domain.AnalysisResult{}, fmt.Errorf("gemini decisions: invalid evidence")
	}
	body, err := json.Marshal(struct {
		Model     string              `json:"model"`
		Input     string              `json:"input"`
		Questions map[string]question `json:"questions"`
	}{c.model, string(evidence), questions()})
	if err != nil {
		return domain.AnalysisResult{}, fmt.Errorf("gemini decisions: invalid request")
	}
	ctx, cancel := context.WithTimeout(ctx, c.requestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL, bytes.NewReader(body))
	if err != nil {
		return domain.AnalysisResult{}, fmt.Errorf("gemini decisions: invalid endpoint")
	}
	req.Header.Set("x-goog-api-key", c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	res, err := c.httpClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return domain.AnalysisResult{}, fmt.Errorf("gemini decisions: request failed: %w", ctx.Err())
		}
		return domain.AnalysisResult{}, fmt.Errorf("gemini decisions: request failed")
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return domain.AnalysisResult{}, fmt.Errorf("gemini decisions: provider returned status %d", res.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, maxResponseBytes+1))
	if err != nil {
		return domain.AnalysisResult{}, fmt.Errorf("gemini decisions: response read failed")
	}
	if len(data) > maxResponseBytes {
		return domain.AnalysisResult{}, fmt.Errorf("gemini decisions: response too large")
	}
	var parsed response
	if err := json.Unmarshal(data, &parsed); err != nil {
		return domain.AnalysisResult{}, fmt.Errorf("gemini decisions: invalid response JSON")
	}
	return parseResponse(parsed, c.model)
}

func parseResponse(r response, model string) (domain.AnalysisResult, error) {
	p, d, u, e := r.Answers["pattern"], r.Answers["direction"], r.Answers["urgency"], r.Answers["evidence"]
	if r.Usage == nil || r.Usage.TotalInputTokens == nil || r.Usage.TotalOutputTokens == nil || *r.Usage.TotalInputTokens < 0 || *r.Usage.TotalOutputTokens != 0 || p.Type != "choice" || d.Type != "choice" || u.Type != "score" || e.Type != "noul" || p.Confidence == nil || d.Confidence == nil || u.Confidence == nil || u.Score == nil || e.Noul == nil || e.Confidence == nil {
		return domain.AnalysisResult{}, fmt.Errorf("gemini decisions: incomplete response")
	}
	patterns := map[string]bool{"traffic_surge": true, "error_burst": true, "latency_degradation": true, "mixed": true, "unclear": true}
	directions := map[string]bool{"traffic_sources": true, "failing_endpoints": true, "latency": true, "more_evidence": true}
	valid := func(x float64, max float64) bool { return !math.IsNaN(x) && !math.IsInf(x, 0) && x >= 0 && x <= max }
	if !patterns[p.Choice] || !directions[d.Choice] || !valid(*p.Confidence, 1) || !valid(*d.Confidence, 1) || !valid(*u.Confidence, 1) || !valid(*e.Confidence, 1) || !valid(*u.Score, 2) || !valid(*e.Noul, 1) {
		return domain.AnalysisResult{}, fmt.Errorf("gemini decisions: invalid answer")
	}
	// The REST response does not report a model version: persist the requested ID.
	return domain.AnalysisResult{Pattern: p.Choice, Direction: d.Choice, Model: model, Urgency: *u.Score, PatternConfidence: *p.Confidence, DirectionConfidence: *d.Confidence, UrgencyConfidence: *u.Confidence, EvidenceProbability: *e.Noul, InputTokens: *r.Usage.TotalInputTokens}, nil
}
