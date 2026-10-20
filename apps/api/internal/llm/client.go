package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"traffic-center/domain"
)

const defaultBaseURL = "https://generativelanguage.googleapis.com/v1beta/models"

// Client implements domain.Explainer using Gemini with a deterministic
// rule-based fallback. It is rate-limited per project so a burst of anomalies
// cannot hammer the API.
type Client struct {
	apiKey          string
	model           string
	baseURL         string
	httpClient      *http.Client
	requestTimeout  time.Duration
	rateLimitWindow time.Duration

	mu          sync.Mutex
	lastRequest map[string]time.Time
}

// New creates a Gemini explainer. An empty API key disables the network call
// and uses the deterministic fallback only.
func New(apiKey, model string) *Client {
	if model == "" {
		model = "gemini-2.0-flash"
	}
	return &Client{
		apiKey:          apiKey,
		model:           model,
		baseURL:         defaultBaseURL,
		httpClient:      &http.Client{Timeout: 5 * time.Second},
		requestTimeout:  4 * time.Second,
		rateLimitWindow: 60 * time.Second,
		lastRequest:     make(map[string]time.Time),
	}
}

// Explain implements domain.Explainer.
func (c *Client) Explain(ctx context.Context, projectID string, anomaly domain.Anomaly, snapshot domain.Snapshot) string {
	if c.apiKey == "" {
		return c.fallback(anomaly, snapshot)
	}
	if !c.allow(projectID) {
		return c.fallback(anomaly, snapshot)
	}

	explanation, err := c.callGemini(ctx, anomaly, snapshot)
	if err != nil || strings.TrimSpace(explanation) == "" {
		return c.fallback(anomaly, snapshot)
	}
	return explanation
}

func (c *Client) allow(projectID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.lastRequest == nil {
		c.lastRequest = make(map[string]time.Time)
	}
	if last, exists := c.lastRequest[projectID]; exists && time.Since(last) < c.rateLimitWindow {
		return false
	}
	c.lastRequest[projectID] = time.Now()
	return true
}

func (c *Client) fallback(anomaly domain.Anomaly, snapshot domain.Snapshot) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("[Rule-Based Analysis] %s (severity: %s). ", anomaly.Message, anomaly.Severity))

	switch anomaly.Type {
	case "high_traffic":
		sb.WriteString(fmt.Sprintf("Traffic reached %.2f RPS across %d unique IPs.", snapshot.RPS, snapshot.UniqueIPs))
	case "high_error_rate":
		sb.WriteString(fmt.Sprintf("Observed %.2f%% error rate with %.2f ms P95 latency.", snapshot.ErrorRate, snapshot.P95))
	case "high_latency":
		sb.WriteString(fmt.Sprintf("P95 latency spiked to %.2f ms (P50: %.2f ms, P99: %.2f ms).", snapshot.P95, snapshot.P50, snapshot.P99))
	case "possible_ip_flood":
		sb.WriteString(fmt.Sprintf("Single client IP is generating %.2f RPS out of total %.2f RPS.", snapshot.TopIPRPS, snapshot.RPS))
	default:
		sb.WriteString(fmt.Sprintf("Current metric value: %.2f (configured threshold: %.2f).", anomaly.Value, anomaly.Threshold))
	}

	if len(snapshot.Endpoints) > 0 {
		var topEndpoint string
		var topCount int
		for endpoint, count := range snapshot.Endpoints {
			if count > topCount {
				topCount = count
				topEndpoint = endpoint
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

func (c *Client) callGemini(ctx context.Context, anomaly domain.Anomaly, snapshot domain.Snapshot) (string, error) {
	prompt := fmt.Sprintf(
		"You are a site reliability engineering AI. In 2 concise sentences, analyze this traffic anomaly:\n"+
			"Anomaly: %s (Severity: %s, Current: %.2f, Threshold: %.2f)\n"+
			"Snapshot: RPS=%.2f, P95=%.2fms, ErrorRate=%.2f%%, TopIPRPS=%.2f, UniqueIPs=%d",
		anomaly.Type, anomaly.Severity, anomaly.Value, anomaly.Threshold,
		snapshot.RPS, snapshot.P95, snapshot.ErrorRate, snapshot.TopIPRPS, snapshot.UniqueIPs,
	)

	body, err := json.Marshal(geminiRequest{
		Contents: []geminiContent{{Parts: []geminiPart{{Text: prompt}}}},
	})
	if err != nil {
		return "", err
	}

	url := fmt.Sprintf("%s/%s:generateContent?key=%s", c.baseURL, c.model, c.apiKey)

	requestCtx, cancel := context.WithTimeout(ctx, c.requestTimeout)
	defer cancel()

	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	request.Header.Set("Content-Type", "application/json")

	response, err := c.httpClient.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("gemini returned status %d", response.StatusCode)
	}

	responseBody, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return "", err
	}

	var parsed geminiResponse
	if err := json.Unmarshal(responseBody, &parsed); err != nil {
		return "", err
	}

	if len(parsed.Candidates) > 0 && len(parsed.Candidates[0].Content.Parts) > 0 {
		return strings.TrimSpace(parsed.Candidates[0].Content.Parts[0].Text), nil
	}
	return "", fmt.Errorf("empty candidate in gemini response")
}
