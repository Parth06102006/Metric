package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"traffic-center/backend/metrics"
)

func sampleSnapshot() metrics.Snapshot {
	return metrics.Snapshot{
		RPS:       150.0,
		P95:       750.0,
		P50:       120.0,
		P99:       1200.0,
		ErrorRate: 15.0,
		TopIPRPS:  45.0,
		UniqueIPs: 10,
		Endpoints: map[string]int{"/api/orders": 80, "/api/users": 20},
	}
}

func sampleAnomaly() AnomalyInfo {
	return AnomalyInfo{
		Type:      "high_error_rate",
		Severity:  "critical",
		Message:   "Error rate 15.00% exceeds limit",
		Value:     15.0,
		Threshold: 5.0,
	}
}

func TestClient_MissingAPIKeyFallsBack(t *testing.T) {
	client := &Client{
		APIKey:          "",
		RateLimitWindow: 60 * time.Second,
	}

	explanation := client.Explain(context.Background(), "proj1", sampleAnomaly(), sampleSnapshot())

	if !strings.HasPrefix(explanation, "[Rule-Based Analysis]") {
		t.Fatalf("expected rule-based fallback, got: %s", explanation)
	}
	if !strings.Contains(explanation, "/api/orders") {
		t.Errorf("expected top endpoint mentioned in fallback analysis, got: %s", explanation)
	}
}

func TestClient_SuccessfulGeminiResponse(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"candidates": [
				{
					"content": {
						"parts": [
							{"text": "Severe error rate spike detected due to cascading upstream 500 errors."}
						]
					}
				}
			]
		}`))
	}))
	defer mockServer.Close()

	client := &Client{
		APIKey:          "test-key",
		Model:           "gemini-1.5-flash",
		BaseURL:         mockServer.URL,
		HTTPClient:      mockServer.Client(),
		RequestTimeout:  1 * time.Second,
		RateLimitWindow: 60 * time.Second,
	}

	explanation := client.Explain(context.Background(), "proj1", sampleAnomaly(), sampleSnapshot())

	expected := "Severe error rate spike detected due to cascading upstream 500 errors."
	if explanation != expected {
		t.Fatalf("expected %q, got %q", expected, explanation)
	}
}

func TestClient_HTTPErrorFallsBack(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error": "rate limit exceeded"}`))
	}))
	defer mockServer.Close()

	client := &Client{
		APIKey:          "test-key",
		Model:           "gemini-1.5-flash",
		BaseURL:         mockServer.URL,
		HTTPClient:      mockServer.Client(),
		RequestTimeout:  1 * time.Second,
		RateLimitWindow: 60 * time.Second,
	}

	explanation := client.Explain(context.Background(), "proj1", sampleAnomaly(), sampleSnapshot())

	if !strings.HasPrefix(explanation, "[Rule-Based Analysis]") {
		t.Fatalf("expected fallback on HTTP 429, got: %s", explanation)
	}
}

func TestClient_TimeoutFallsBack(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond) // Exceeds 20ms client timeout
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"candidates": [{"content": {"parts": [{"text": "late"}]}}]}`))
	}))
	defer mockServer.Close()

	client := &Client{
		APIKey:          "test-key",
		Model:           "gemini-1.5-flash",
		BaseURL:         mockServer.URL,
		HTTPClient:      mockServer.Client(),
		RequestTimeout:  20 * time.Millisecond,
		RateLimitWindow: 60 * time.Second,
	}

	explanation := client.Explain(context.Background(), "proj1", sampleAnomaly(), sampleSnapshot())

	if !strings.HasPrefix(explanation, "[Rule-Based Analysis]") {
		t.Fatalf("expected fallback on timeout, got: %s", explanation)
	}
}

func TestClient_PerProjectRateLimiting(t *testing.T) {
	callCount := 0
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"candidates": [{"content": {"parts": [{"text": "AI response"}]}}]}`))
	}))
	defer mockServer.Close()

	client := &Client{
		APIKey:          "test-key",
		Model:           "gemini-1.5-flash",
		BaseURL:         mockServer.URL,
		HTTPClient:      mockServer.Client(),
		RequestTimeout:  1 * time.Second,
		RateLimitWindow: 60 * time.Second,
	}

	// First call for proj1 should hit API
	resp1 := client.Explain(context.Background(), "proj1", sampleAnomaly(), sampleSnapshot())
	if resp1 != "AI response" {
		t.Fatalf("expected AI response on first call, got: %s", resp1)
	}
	if callCount != 1 {
		t.Fatalf("expected 1 API call, got %d", callCount)
	}

	// Immediate second call for proj1 within 60s window must fall back without making another API call
	resp2 := client.Explain(context.Background(), "proj1", sampleAnomaly(), sampleSnapshot())
	if !strings.HasPrefix(resp2, "[Rule-Based Analysis]") {
		t.Fatalf("expected fallback on rate-limited call, got: %s", resp2)
	}
	if callCount != 1 {
		t.Fatalf("expected call count to remain 1, got %d", callCount)
	}

	// A different project should still be allowed its own API call
	resp3 := client.Explain(context.Background(), "proj2", sampleAnomaly(), sampleSnapshot())
	if resp3 != "AI response" {
		t.Fatalf("expected AI response for proj2, got: %s", resp3)
	}
	if callCount != 2 {
		t.Fatalf("expected call count to be 2 for different project, got %d", callCount)
	}
}
