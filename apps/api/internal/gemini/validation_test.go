package gemini

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseResponseRejectsInvalidAnswers(t *testing.T) {
	for name, body := range map[string]string{
		"unknown pattern":             strings.Replace(goodResponse, `"traffic_surge"`, `"invented"`, 1),
		"unknown direction":           strings.Replace(goodResponse, `"traffic_sources"`, `"invented"`, 1),
		"probability out of range":    strings.Replace(goodResponse, `"noul":0.6`, `"noul":1.5`, 1),
		"missing evidence confidence": strings.Replace(goodResponse, `,"confidence":0.6`, ``, 1),
		"missing token usage":         strings.Replace(goodResponse, `"total_input_tokens":123,`, ``, 1),
		"unexpected output tokens":    strings.Replace(goodResponse, `"total_output_tokens":0`, `"total_output_tokens":1`, 1),
		"urgency out of range":        strings.Replace(goodResponse, `"score":1.5`, `"score":3`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			var r response
			if err := json.Unmarshal([]byte(body), &r); err != nil {
				t.Fatal(err)
			}
			if _, err := parseResponse(r, DefaultModel); err == nil {
				t.Fatal("invalid answer accepted")
			}
		})
	}
}
