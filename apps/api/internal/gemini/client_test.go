package gemini

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"traffic-center/domain"
)

const goodResponse = `{"answers":{"pattern":{"type":"choice","choice":"traffic_surge","confidence":0.9},"direction":{"type":"choice","choice":"traffic_sources","confidence":0.8},"urgency":{"type":"score","score":1.5,"confidence":0.7},"evidence":{"type":"noul","noul":0.6,"confidence":0.6}},"usage":{"total_input_tokens":123,"total_output_tokens":0}}`

func TestDiagnoseSendsGeminiDecisionsAndMapsAnswers(t *testing.T) {
	if defaultBaseURL != "https://generativelanguage.googleapis.com/v1beta/decisions" {
		t.Fatal("wrong endpoint")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/v1beta/decisions" || r.URL.RawQuery != "" || r.Header.Get("x-goog-api-key") != "secret" || r.Header.Get("Authorization") != "" {
			t.Error("incorrect endpoint or authentication")
		}
		var req struct {
			Model     string              `json:"model"`
			Input     string              `json:"input"`
			Questions map[string]question `json:"questions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		var evidence map[string]any
		if err := json.Unmarshal([]byte(req.Input), &evidence); err != nil {
			t.Error(err)
			return
		}
		if req.Model != DefaultModel || evidence["trigger_type"] != "high_traffic" || evidence["rps"] != float64(50) || evidence["baseline"] == nil {
			t.Error("incorrect input")
		}
		for key, typ := range map[string]string{"pattern": "choice", "direction": "choice", "urgency": "score", "evidence": "noul"} {
			if q := req.Questions[key]; q.Type != typ || q.Instructions == "" {
				t.Errorf("invalid question %s", key)
			}
		}
		if len(req.Questions) != 4 || strings.Contains(req.Input, "/secret") || strings.Contains(req.Input, "192.0.2.1") {
			t.Error("unexpected question or leaked raw identifier")
		}
		_, _ = w.Write([]byte(goodResponse))
	}))
	defer srv.Close()
	c := New("secret", "")
	c.baseURL = srv.URL + "/v1beta/decisions"
	got, err := c.Diagnose(context.Background(), domain.IncidentEvidence{TriggerType: "high_traffic", RPS: 50, Baseline: &domain.BaselineEvidence{Samples: 3, RPS: 10}})
	if err != nil {
		t.Fatal(err)
	}
	want := domain.AnalysisResult{Pattern: "traffic_surge", Direction: "traffic_sources", Model: DefaultModel, Urgency: 1.5, PatternConfidence: 0.9, DirectionConfidence: 0.8, UrgencyConfidence: 0.7, EvidenceProbability: 0.6, InputTokens: 123}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v want %+v", got, want)
	}
}

func TestRedirectDoesNotLeakKey(t *testing.T) {
	received := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { received = true }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	c := New("secret", "")
	c.baseURL = source.URL
	if _, err := c.Diagnose(context.Background(), domain.IncidentEvidence{}); err == nil {
		t.Fatal("redirect accepted")
	}
	if received {
		t.Fatal("followed redirect with credentials")
	}
}
