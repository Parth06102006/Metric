package ingest

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"traffic-center/backend/metrics"
)

func newTestHandler() (*Handler, *metrics.Buffer) {
	buffer := metrics.NewBuffer(time.Minute)
	return &Handler{
		Projects: map[string]ProjectTarget{
			"demo": {APIKey: "test-secret", Buffer: buffer},
		},
	}, buffer
}

func sendRequest(h http.Handler, key string, body any) *httptest.ResponseRecorder {
	data, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/ingest/demo", bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", key)

	recorder := httptest.NewRecorder()
	h.ServeHTTP(recorder, req)
	return recorder
}

func validRecord() metrics.LogRecord {
	return metrics.LogRecord{
		Ts:           time.Now(),
		Endpoint:     "/login",
		Method:       "GET",
		Status:       200,
		LatencyMs:    120,
		IP:           "127.0.0.1",
		PayloadBytes: 100,
	}
}

func TestIngestAcceptsValidBatch(t *testing.T) {
	h, buffer := newTestHandler()
	recorder := sendRequest(h, "test-secret", map[string]any{
		"logs": []metrics.LogRecord{validRecord()},
	})

	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d: %s", recorder.Code, http.StatusAccepted, recorder.Body)
	}

	if got := len(buffer.Window(time.Now())); got != 1 {
		t.Fatalf("buffer has %d records, want 1", got)
	}
}

func TestIngestRejectsWrongAPIKey(t *testing.T) {
	h, buffer := newTestHandler()
	recorder := sendRequest(h, "wrong-key", map[string]any{
		"logs": []metrics.LogRecord{validRecord()},
	})

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusUnauthorized)
	}

	if got := len(buffer.Window(time.Now())); got != 0 {
		t.Fatalf("buffer has %d records, want 0", got)
	}
}

func TestIngestRejectsInvalidBatchAtomically(t *testing.T) {
	h, buffer := newTestHandler()
	valid := validRecord()
	invalid := validRecord()
	invalid.Status = 700

	recorder := sendRequest(h, "test-secret", map[string]any{
		"logs": []metrics.LogRecord{valid, invalid},
	})

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}

	if got := len(buffer.Window(time.Now())); got != 0 {
		t.Fatalf("invalid batch partially entered buffer: %d records", got)
	}
}

func TestIngestRejectsGET(t *testing.T) {
	h, _ := newTestHandler()
	req := httptest.NewRequest(http.MethodGet, "/ingest/demo", nil)
	recorder := httptest.NewRecorder()

	h.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusMethodNotAllowed)
	}
}
