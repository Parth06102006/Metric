package server_test

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"traffic-center/anomaly"
	anomalysqlite "traffic-center/anomaly/repository/sqlite"
	"traffic-center/domain"
	"traffic-center/ingest"
	"traffic-center/internal/config"
	"traffic-center/internal/server"
	"traffic-center/internal/sqlite"
	"traffic-center/internal/ws"
	"traffic-center/metrics"
	metricsmemory "traffic-center/metrics/repository/memory"
	metricssqlite "traffic-center/metrics/repository/sqlite"
	"traffic-center/project"
	projectsqlite "traffic-center/project/repository/sqlite"
	"traffic-center/threshold"
	thresholdsqlite "traffic-center/threshold/repository/sqlite"
)

type createdProject struct {
	ProjectID string `json:"project_id"`
	Name      string `json:"name"`
	APIKey    string `json:"api_key"`
}

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()

	db, err := sqlite.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	projectRepo := projectsqlite.New(db)
	thresholdRepo := thresholdsqlite.New(db)
	recordRepo := metricsmemory.New(60 * time.Second)
	snapshotRepo := metricssqlite.New(db)
	anomalyRepo := anomalysqlite.New(db)
	hub := ws.NewHub()

	projectUsecase := project.NewUsecase(projectRepo)
	thresholdUsecase := threshold.NewUsecase(thresholdRepo, projectRepo)
	metricsUsecase := metrics.NewUsecase(recordRepo, snapshotRepo, 60*time.Second)
	ingestUsecase := ingest.NewUsecase(projectRepo, recordRepo)
	anomalyUsecase := anomaly.NewUsecase(anomalyRepo, thresholdRepo, nil, hub, time.Minute)

	cfg := config.Config{
		AuthMode:        "dev",
		DevEmail:        "dev@test",
		AuthEmailHeader: "X-Auth-Request-Email",
		AuthUserHeader:  "X-Auth-Request-User",
		TrustProxy:      false,
		MetricsWindow:   60 * time.Second,
		APITimeout:      5 * time.Second,
		AdminEmails:     map[string]struct{}{},
		AllowedOrigins:  map[string]struct{}{},
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := server.New(cfg, logger, server.Deps{
		Projects:    projectUsecase,
		Thresholds:  thresholdUsecase,
		Metrics:     metricsUsecase,
		Ingest:      ingestUsecase,
		Anomalies:   anomalyUsecase,
		ProjectRepo: projectRepo,
		RecordRepo:  recordRepo,
		Hub:         hub,
	})

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server
}

func doJSON(t *testing.T, method, url string, body any, headers map[string]string) (*http.Response, []byte) {
	t.Helper()

	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		reader = bytes.NewReader(payload)
	}

	request, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer response.Body.Close()

	responseBody, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return response, responseBody
}

func createProject(t *testing.T, baseURL, name string) createdProject {
	t.Helper()

	response, body := doJSON(t, http.MethodPost, baseURL+"/api/projects", map[string]string{"name": name}, nil)
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create project status = %d, body = %s", response.StatusCode, body)
	}

	var project createdProject
	if err := json.Unmarshal(body, &project); err != nil {
		t.Fatalf("decode project: %v", err)
	}
	if project.ProjectID == "" || project.APIKey == "" {
		t.Fatalf("incomplete project: %+v", project)
	}
	return project
}

func ingestRecords(t *testing.T, baseURL, projectID, apiKey string, records []domain.LogRecord) {
	t.Helper()

	response, body := doJSON(t, http.MethodPost, baseURL+"/ingest/"+projectID,
		map[string]any{"logs": records},
		map[string]string{"X-API-Key": apiKey},
	)
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("ingest status = %d, body = %s", response.StatusCode, body)
	}
}

func sampleRecords() []domain.LogRecord {
	now := time.Now().UTC()
	return []domain.LogRecord{
		{Ts: now, Endpoint: "/checkout", Method: "POST", Status: 200, LatencyMs: 10, IP: "203.0.113.7", PayloadBytes: 100},
		{Ts: now, Endpoint: "/checkout", Method: "POST", Status: 500, LatencyMs: 30, IP: "203.0.113.7", PayloadBytes: 300},
	}
}

func TestProjectLifecycle(t *testing.T) {
	server := newTestServer(t)

	project := createProject(t, server.URL, "Checkout API")
	ingestRecords(t, server.URL, project.ProjectID, project.APIKey, sampleRecords())

	response, body := doJSON(t, http.MethodGet, server.URL+"/api/projects/"+project.ProjectID+"/metrics", nil, nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("metrics status = %d, body = %s", response.StatusCode, body)
	}
	var snapshot domain.Snapshot
	if err := json.Unmarshal(body, &snapshot); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	if snapshot.UniqueIPs != 1 || snapshot.RPS <= 0 || snapshot.ErrorRate != 50 {
		t.Fatalf("unexpected snapshot: %+v", snapshot)
	}

	response, body = doJSON(t, http.MethodGet, server.URL+"/api/projects/"+project.ProjectID+"/thresholds", nil, nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("thresholds status = %d, body = %s", response.StatusCode, body)
	}
	var thresholds domain.Threshold
	if err := json.Unmarshal(body, &thresholds); err != nil {
		t.Fatalf("decode thresholds: %v", err)
	}
	if thresholds != domain.DefaultThreshold() {
		t.Fatalf("thresholds = %+v, want defaults", thresholds)
	}

	updated := domain.Threshold{P95Ms: 250, ErrorRate: 2, RPSMax: 80, ReqPerIPMax: 10}
	response, body = doJSON(t, http.MethodPut, server.URL+"/api/projects/"+project.ProjectID+"/thresholds", updated, nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("update thresholds status = %d, body = %s", response.StatusCode, body)
	}

	response, body = doJSON(t, http.MethodGet, server.URL+"/api/projects/"+project.ProjectID+"/thresholds", nil, nil)
	if err := json.Unmarshal(body, &thresholds); err != nil {
		t.Fatalf("decode thresholds: %v", err)
	}
	if thresholds != updated {
		t.Fatalf("thresholds = %+v, want %+v", thresholds, updated)
	}

	response, body = doJSON(t, http.MethodGet, server.URL+"/api/projects/"+project.ProjectID+"/anomalies", nil, nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("anomalies status = %d, body = %s", response.StatusCode, body)
	}
	if strings.TrimSpace(string(body)) != "[]" {
		t.Fatalf("anomalies = %s, want []", body)
	}

	response, body = doJSON(t, http.MethodGet, server.URL+"/api/projects", nil, nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("list projects status = %d, body = %s", response.StatusCode, body)
	}
	if !strings.Contains(string(body), project.ProjectID) {
		t.Fatalf("list projects missing project: %s", body)
	}

	response, body = doJSON(t, http.MethodGet, server.URL+"/api/auth/me", nil, nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("me status = %d, body = %s", response.StatusCode, body)
	}
	if !strings.Contains(string(body), "dev@test") {
		t.Fatalf("me body = %s", body)
	}
}

func TestIngestRejectsBadKeyAndUnknownRoutes(t *testing.T) {
	server := newTestServer(t)
	project := createProject(t, server.URL, "Auth Check")

	response, _ := doJSON(t, http.MethodPost, server.URL+"/ingest/"+project.ProjectID,
		map[string]any{"logs": sampleRecords()},
		map[string]string{"X-API-Key": "wrong"},
	)
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("ingest status = %d, want 401", response.StatusCode)
	}

	response, _ = doJSON(t, http.MethodGet, server.URL+"/api/projects/missing", nil, nil)
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("missing project status = %d, want 404", response.StatusCode)
	}

	response, _ = doJSON(t, http.MethodGet, server.URL+"/health", nil, nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("health status = %d, want 200", response.StatusCode)
	}

	response, _ = doJSON(t, http.MethodGet, server.URL+"/nope", nil, nil)
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown route status = %d, want 404", response.StatusCode)
	}
}

func TestWebSocketInitialSnapshot(t *testing.T) {
	server := newTestServer(t)
	project := createProject(t, server.URL, "Live")
	ingestRecords(t, server.URL, project.ProjectID, project.APIKey, sampleRecords())

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws/" + project.ProjectID
	connection, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer connection.Close()

	_ = connection.SetReadDeadline(time.Now().Add(2 * time.Second))

	var envelope struct {
		EventType string          `json:"event_type"`
		ProjectID string          `json:"project_id"`
		Data      domain.Snapshot `json:"data"`
	}
	if err := connection.ReadJSON(&envelope); err != nil {
		t.Fatalf("read websocket: %v", err)
	}

	if envelope.EventType != string(ws.EventMetricSnapshot) {
		t.Fatalf("event type = %q", envelope.EventType)
	}
	if envelope.ProjectID != project.ProjectID {
		t.Fatalf("project id = %q", envelope.ProjectID)
	}
	if envelope.Data.UniqueIPs != 1 {
		t.Fatalf("initial snapshot = %+v", envelope.Data)
	}
}

func TestWebSocketUnknownProject(t *testing.T) {
	server := newTestServer(t)

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws/missing"
	_, response, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err == nil {
		t.Fatal("expected dial failure")
	}
	if response == nil || response.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %v, want 404", response)
	}
}

func TestMethodNotAllowed(t *testing.T) {
	server := newTestServer(t)

	response, body := doJSON(t, http.MethodDelete, server.URL+"/api/projects", nil, nil)
	if response.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d (body %s), want 405", response.StatusCode, body)
	}
}
