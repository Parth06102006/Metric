package jev

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestDiagnoseSendsOpenRouterDecisionsAndMapsAnswers(t *testing.T) {
	if defaultBaseURL != "https://openrouter.ai/api/alpha/decisions" {
		t.Fatalf("unexpected provider endpoint: %s", defaultBaseURL)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/alpha/decisions" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer secret" || r.Header.Get("Content-Type") != "application/json" {
			t.Error("incorrect request headers")
		}
		var req struct {
			Model     string         `json:"model"`
			State     map[string]any `json:"state"`
			Questions map[string]struct {
				Type         string `json:"type"`
				Instructions string `json:"instructions"`
				Criteria     any    `json:"criteria"`
			} `json:"questions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		if req.Model != "typesafe/jev-1.14" || req.State["trigger_type"] != "high_traffic" || req.State["rps"] != float64(50) || req.State["baseline"] == nil {
			t.Errorf("bad request model/state: %+v", req)
		}
		expected := map[string]string{"pattern": "choice", "direction": "choice", "urgency": "score", "evidence": "noul"}
		if len(req.Questions) != len(expected) {
			t.Errorf("questions = %v", req.Questions)
		}
		for name, typ := range expected {
			q := req.Questions[name]
			if q.Type != typ || q.Instructions == "" {
				t.Errorf("bad question %s: %+v", name, q)
			}
		}
		pattern := req.Questions["pattern"].Criteria.(map[string]any)
		direction := req.Questions["direction"].Criteria.(map[string]any)
		for _, label := range []string{"traffic_surge", "error_burst", "latency_degradation", "mixed", "unclear"} {
			if pattern[label] == nil {
				t.Errorf("missing pattern %s", label)
			}
		}
		for _, label := range []string{"traffic_sources", "failing_endpoints", "latency", "more_evidence"} {
			if direction[label] == nil {
				t.Errorf("missing direction %s", label)
			}
		}
		if len(pattern) != 5 || len(direction) != 4 || len(req.Questions["urgency"].Criteria.([]any)) != 3 {
			t.Error("incorrect question criteria")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"gen-dec-test","provider":"TypeSafe","model":"typesafe/jev-1.14","answers":{"pattern":{"type":"choice","choice":"traffic_surge","confidence":0.9,"probabilities":{"traffic_surge":0.8,"error_burst":0.05,"latency_degradation":0.05,"mixed":0.05,"unclear":0.05}},"direction":{"type":"choice","choice":"traffic_sources","confidence":0.8,"probabilities":{"traffic_sources":0.7,"failing_endpoints":0.1,"latency":0.1,"more_evidence":0.1}},"urgency":{"type":"score","score":1.5,"confidence":0.7,"legend":{"0":"low","1":"medium","2":"high"},"probabilities":{"0":0.1,"1":0.3,"2":0.6}},"evidence":{"type":"noul","noul":0.6}},"usage":{"input_tokens":123,"output_tokens":5,"cost":0.00001}}`))
	}))
	defer srv.Close()
	c := New("secret", "")
	c.baseURL = srv.URL + "/api/alpha/decisions"
	got, err := c.Diagnose(context.Background(), State{TriggerType: "high_traffic", RPS: 50, Baseline: &Baseline{Samples: 3, RPS: 10}})
	if err != nil {
		t.Fatal(err)
	}
	want := Result{Pattern: "traffic_surge", Direction: "traffic_sources", Model: "typesafe/jev-1.14", Urgency: 1.5, PatternConfidence: 0.9, DirectionConfidence: 0.8, UrgencyConfidence: 0.7, EvidenceProbability: 0.6, InputTokens: 123}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v want %+v", got, want)
	}
}
