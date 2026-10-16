package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"traffic-center/backend/metrics"
)

// AnomalyInfo captures what triggered the alert.
type AnomalyInfo struct {
	Type      string
	Severity  string
	Message   string
	Value     float64
	Threshold float64
}

// Explainer generates human-readable incident explanations.
type Explainer interface {
	Explain(ctx context.Context, projectID string, anom AnomalyInfo, snap metrics.Snapshot) string
}

// Client coordinates Gemini API calls with strict rate limiting and deterministic fallback.
type Client struct {
	APIKey          string
	Model           string
	BaseURL         string
	HTTPClient      *http.Client
	RequestTimeout  time.Duration
	RateLimitWindow time.Duration

	mu          sync.Mutex
	lastRequest map[string]time.Time
}

// NewClient initializes a Gemini client from environment or defaults.
func NewClient() *Client {
	apiKey := os.Getenv("GEMINI_API_KEY")
	model := os.Getenv("GEMINI_MODEL")
	if model == "" {
		model = "gemini-1.5-flash"
	}

	return &Client{
		APIKey:          apiKey,
		Model:           model,
		BaseURL:         "https://generativelanguage.googleapis.com/v1beta/models",
		HTTPClient:      &http.Client{Timeout: 3 * time.Second},
		RequestTimeout:  3 * time.Second,
		RateLimitWindow: 60 * time.Second,
		lastRequest:     make(map[string]time.Time),
	}
}

// Explain generates an incident explanation, using Gemini API when available or falling back to deterministic rules.
func (c *Client) Explain(ctx context.Context, projectID string, anom AnomalyInfo, snap metrics.Snapshot) string {
	if c.APIKey == "" {
		return c.FallbackExplain(anom, snap)
	}

	// Check per-project rate limit: 1 request per 60 seconds
	if !c.allowProjectRequest(projectID) {
		return c.FallbackExplain(anom, snap)
	}

	explanation, err := c.callGemini(ctx, anom, snap)
	if err != nil || strings.TrimSpace(explanation) == "" {
		return c.FallbackExplain(anom, snap)
	}

	return explanation
}

func (c *Client) allowProjectRequest(projectID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.lastRequest == nil {
		c.lastRequest = make(map[string]time.Time)
	}

	last, exists := c.lastRequest[projectID]
	if exists && time.Since(last) < c.RateLimitWindow {
		return false
	}

	c.lastRequest[projectID] = time.Now()
	return true
}

// FallbackExplain generates a deterministic rule-based summary without external dependencies.
func (c *Client) FallbackExplain(anom AnomalyInfo, snap metrics.Snapshot) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("[Rule-Based Analysis] %s (severity: %s). ", anom.Message, anom.Severity))

	switch anom.Type {
	case "high_traffic":
		sb.WriteString(fmt.Sprintf("Traffic reached %.2f RPS across %d unique IPs.", snap.RPS, snap.UniqueIPs))
	case "high_error_rate":
		sb.WriteString(fmt.Sprintf("Observed %.2f%% error rate with %.2f ms P95 latency.", snap.ErrorRate, snap.P95))
	case "high_latency":
		sb.WriteString(fmt.Sprintf("P95 latency spiked to %.2f ms (P50: %.2f ms, P99: %.2f ms).", snap.P95, snap.P50, snap.P99))
	case "possible_ip_flood":
		sb.WriteString(fmt.Sprintf("Single client IP is generating %.2f RPS out of total %.2f RPS.", snap.TopIPRPS, snap.RPS))
	default:
		sb.WriteString(fmt.Sprintf("Current metric value: %.2f (configured threshold: %.2f).", anom.Value, anom.Threshold))
	}

	if len(snap.Endpoints) > 0 {
		var topEndpoint string
		var topCount int
		for ep, count := range snap.Endpoints {
			if count > topCount {
				topCount = count
				topEndpoint = ep
			}
		}
		if topEndpoint != "" {
			sb.WriteString(fmt.Sprintf(" Most active endpoint: %s (%d requests).", topEndpoint, topCount))
		}
	}

	return sb.String()
}

type geminiPart struct {
	Text string `json:"text"`
}

type geminiContent struct {
	Parts []geminiPart `json:"parts"`
}

type geminiRequest struct {
	Contents []geminiContent `json:"contents"`
}

type geminiCandidate struct {
	Content geminiContent `json:"content"`
}

type geminiResponse struct {
	Candidates []geminiCandidate `json:"candidates"`
}

func (c *Client) callGemini(ctx context.Context, anom AnomalyInfo, snap metrics.Snapshot) (string, error) {
	prompt := fmt.Sprintf(
		"You are a site reliability engineering AI. In 2 concise sentences, analyze this traffic anomaly:\n"+
			"Anomaly: %s (Severity: %s, Current: %.2f, Threshold: %.2f)\n"+
			"Snapshot: RPS=%.2f, P95=%.2fms, ErrorRate=%.2f%%, TopIPRPS=%.2f, UniqueIPs=%d",
		anom.Type, anom.Severity, anom.Value, anom.Threshold,
		snap.RPS, snap.P95, snap.ErrorRate, snap.TopIPRPS, snap.UniqueIPs,
	)

	reqBody := geminiRequest{
		Contents: []geminiContent{
			{Parts: []geminiPart{{Text: prompt}}},
		},
	}

	jsonBytes, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}

	url := fmt.Sprintf("%s/%s:generateContent?key=%s", c.BaseURL, c.Model, c.APIKey)

	reqCtx, cancel := context.WithTimeout(ctx, c.RequestTimeout)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(reqCtx, http.MethodPost, url, bytes.NewReader(jsonBytes))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTPClient.Do(httpReq)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("gemini api returned status: %d", resp.StatusCode)
	}

	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}

	var geminiResp geminiResponse
	if err := json.Unmarshal(bodyBytes, &geminiResp); err != nil {
		return "", err
	}

	if len(geminiResp.Candidates) > 0 && len(geminiResp.Candidates[0].Content.Parts) > 0 {
		return strings.TrimSpace(geminiResp.Candidates[0].Content.Parts[0].Text), nil
	}

	return "", fmt.Errorf("empty candidate in gemini response")
}
