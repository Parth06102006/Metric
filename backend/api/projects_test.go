package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"traffic-center/backend/metrics"
	"traffic-center/backend/store"
)

type mockBufferProvider struct {
	buffers map[string]*metrics.Buffer
}

func (m *mockBufferProvider) GetBuffer(projectID string) (*metrics.Buffer, bool) {
	b, ok := m.buffers[projectID]
	return b, ok
}

func setupAPITestHandler(t *testing.T) (*Handler, *store.Store, *mockBufferProvider) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "api_test.db")
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	bp := &mockBufferProvider{
		buffers: make(map[string]*metrics.Buffer),
	}

	h := &Handler{
		Store:   s,
		Buffers: bp,
	}

	return h, s, bp
}

func TestAPI_CreateAndGetProject(t *testing.T) {
	h, _, _ := setupAPITestHandler(t)

	// POST /api/projects
	body := []byte(`{"name":"Frontend App"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/projects", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201 Created, got %d: %s", w.Code, w.Body.String())
	}

	var createResp createProjectResponse
	if err := json.NewDecoder(w.Body).Decode(&createResp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if createResp.ProjectID == "" || createResp.APIKey == "" {
		t.Fatalf("expected project ID and API key in response: %+v", createResp)
	}

	// GET /api/projects/{id}
	getReq := httptest.NewRequest(http.MethodGet, "/api/projects/"+createResp.ProjectID, nil)
	getW := httptest.NewRecorder()

	h.ServeHTTP(getW, getReq)

	if getW.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", getW.Code, getW.Body.String())
	}

	var proj store.Project
	if err := json.NewDecoder(getW.Body).Decode(&proj); err != nil {
		t.Fatalf("decode project: %v", err)
	}
	if proj.ID != createResp.ProjectID || proj.Name != "Frontend App" {
		t.Fatalf("unexpected project data: %+v", proj)
	}
}

func TestAPI_GetAndUpdateThresholds(t *testing.T) {
	h, s, _ := setupAPITestHandler(t)

	proj, _, err := s.CreateProject("Threshold Project")
	if err != nil {
		t.Fatalf("CreateProject failed: %v", err)
	}

	// GET /api/projects/{id}/thresholds
	req := httptest.NewRequest(http.MethodGet, "/api/projects/"+proj.ID+"/thresholds", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", w.Code)
	}

	// PUT /api/projects/{id}/thresholds
	updateBody := []byte(`{"p95_ms":500,"error_rate":2.5,"rps_max":50,"req_per_ip_max":15}`)
	putReq := httptest.NewRequest(http.MethodPut, "/api/projects/"+proj.ID+"/thresholds", bytes.NewReader(updateBody))
	putReq.Header.Set("Content-Type", "application/json")
	putW := httptest.NewRecorder()
	h.ServeHTTP(putW, putReq)

	if putW.Code != http.StatusOK {
		t.Fatalf("expected 200 OK on PUT, got %d: %s", putW.Code, putW.Body.String())
	}

	var th store.Thresholds
	if err := json.NewDecoder(putW.Body).Decode(&th); err != nil {
		t.Fatalf("decode thresholds: %v", err)
	}
	if th.P95Ms != 500 || th.ErrorRate != 2.5 || th.RPSMax != 50 || th.ReqPerIPMax != 15 {
		t.Fatalf("unexpected updated thresholds: %+v", th)
	}
}

func TestAPI_GetMetrics(t *testing.T) {
	h, s, bp := setupAPITestHandler(t)

	proj, _, err := s.CreateProject("Metrics Project")
	if err != nil {
		t.Fatalf("CreateProject failed: %v", err)
	}

	buf := metrics.NewBuffer(60 * time.Second)
	buf.Add(metrics.LogRecord{
		Ts:        time.Now(),
		Endpoint:  "/api/data",
		Method:    "GET",
		Status:    200,
		LatencyMs: 40.0,
		IP:        "192.0.2.1",
	})
	bp.buffers[proj.ID] = buf

	req := httptest.NewRequest(http.MethodGet, "/api/projects/"+proj.ID+"/metrics", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", w.Code)
	}

	var snapshot metrics.Snapshot
	if err := json.NewDecoder(w.Body).Decode(&snapshot); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	if snapshot.RPS <= 0 || snapshot.UniqueIPs != 1 {
		t.Fatalf("unexpected snapshot data: %+v", snapshot)
	}
}
