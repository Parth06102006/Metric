package jev

import (
	"testing"
)

func TestParseResponseRejectsUnknownAndOutOfRangeAnswers(t *testing.T) {
	value := 0.8
	score := 1.0
	tokens := 10
	r := response{Model: "jev", Usage: &struct {
		InputTokens  *int `json:"input_tokens"`
		OutputTokens *int `json:"output_tokens"`
	}{&tokens, &tokens}, Answers: map[string]answer{
		"pattern":   {Type: "choice", Choice: "invented", Confidence: &value},
		"direction": {Type: "choice", Choice: "latency", Confidence: &value},
		"urgency":   {Type: "score", Score: &score, Confidence: &value},
		"evidence":  {Type: "noul", Noul: &value},
	}}
	if _, err := parseResponse(r); err == nil {
		t.Fatal("accepted unknown pattern")
	}
	a := r.Answers["pattern"]
	a.Choice = "latency_degradation"
	r.Answers["pattern"] = a
	bad := 1.5
	a = r.Answers["evidence"]
	a.Noul = &bad
	r.Answers["evidence"] = a
	if _, err := parseResponse(r); err == nil {
		t.Fatal("accepted out-of-range probability")
	}
}
