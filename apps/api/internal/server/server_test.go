package server_test

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"traffic-center/anomaly"
	anomalysqlite "traffic-center/anomaly/repository/sqlite"
	"traffic-center/audit"
	auditsqlite "traffic-center/audit/repository/sqlite"
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
	projectLifecycle := project.NewLifecycle(projectRepo, recordRepo, hub)
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
	auditUsecase := audit.NewUsecase(auditsqlite.New(db), logger)
	handler := server.New(cfg, logger, server.Deps{
		Projects:    projectUsecase,
		Lifecycle:   projectLifecycle,
		Audit:       auditUsecase,
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
	if response.StatusCode != http.StatusOK {
		t.Fatalf("get updated thresholds status = %d, body = %s", response.StatusCode, body)
	}
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

func newProxyTestServer(t *testing.T, adminEmails []string) (*httptest.Server, *sqlite.RepositoryProxyHelper) {
	t.Helper()
	return newProxyTestServerWithConfig(t, adminEmails, nil)
}

func newProxyTestServerWithConfig(t *testing.T, adminEmails []string, configure func(*config.Config)) (*httptest.Server, *sqlite.RepositoryProxyHelper) {
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
	projectLifecycle := project.NewLifecycle(projectRepo, recordRepo, hub)
	thresholdUsecase := threshold.NewUsecase(thresholdRepo, projectRepo)
	metricsUsecase := metrics.NewUsecase(recordRepo, snapshotRepo, 60*time.Second)
	ingestUsecase := ingest.NewUsecase(projectRepo, recordRepo)
	anomalyUsecase := anomaly.NewUsecase(anomalyRepo, thresholdRepo, nil, hub, time.Minute)

	adminsMap := make(map[string]struct{})
	for _, e := range adminEmails {
		adminsMap[strings.ToLower(e)] = struct{}{}
	}

	cfg := config.Config{
		AuthMode:        "proxy",
		AuthEmailHeader: "X-Auth-Request-Email",
		AuthUserHeader:  "X-Auth-Request-User",
		TrustProxy:      false,
		MetricsWindow:   60 * time.Second,
		APITimeout:      5 * time.Second,
		AdminEmails:     adminsMap,
		AllowedOrigins:  map[string]struct{}{},
	}
	if configure != nil {
		configure(&cfg)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	auditUsecase := audit.NewUsecase(auditsqlite.New(db), logger)
	handler := server.New(cfg, logger, server.Deps{
		Projects:    projectUsecase,
		Lifecycle:   projectLifecycle,
		Audit:       auditUsecase,
		Thresholds:  thresholdUsecase,
		Metrics:     metricsUsecase,
		Ingest:      ingestUsecase,
		Anomalies:   anomalyUsecase,
		ProjectRepo: projectRepo,
		RecordRepo:  recordRepo,
		Hub:         hub,
	})

	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv, &sqlite.RepositoryProxyHelper{DB: db}
}

func TestProjectOwnershipAndAuthorization(t *testing.T) {
	server, helper := newProxyTestServer(t, []string{"admin@example.com"})

	headersUserA := map[string]string{"X-Auth-Request-Email": "alice@example.com"}
	headersUserB := map[string]string{"X-Auth-Request-Email": "bob@example.com"}
	headersAdmin := map[string]string{"X-Auth-Request-Email": "admin@example.com"}
	updatedThreshold := domain.Threshold{P95Ms: 500, ErrorRate: 1, RPSMax: 50, ReqPerIPMax: 10}

	// Setup: Admin creates Project B and assigns it to Bob through the
	// admin-only ownership route.
	resp, body := doJSON(t, http.MethodPost, server.URL+"/api/projects", map[string]string{"name": "Bob Project"}, headersAdmin)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create project B status = %d, body = %s", resp.StatusCode, body)
	}
	var projectB createdProject
	if err := json.Unmarshal(body, &projectB); err != nil {
		t.Fatalf("decode project B: %v", err)
	}
	resp, body = doJSON(t, http.MethodPatch, server.URL+"/api/projects/"+projectB.ProjectID,
		map[string]string{"owner_email": "Bob@Example.com"}, headersAdmin)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("assign owner status = %d, body = %s", resp.StatusCode, body)
	}
	var assigned struct {
		OwnerEmail string `json:"owner_email"`
	}
	if err := json.Unmarshal(body, &assigned); err != nil {
		t.Fatalf("decode assigned project: %v", err)
	}
	if assigned.OwnerEmail != "bob@example.com" {
		t.Fatalf("owner = %q, want normalized bob@example.com", assigned.OwnerEmail)
	}

	// Non-admins cannot reassign ownership.
	resp, _ = doJSON(t, http.MethodPatch, server.URL+"/api/projects/"+projectB.ProjectID,
		map[string]string{"owner_email": "alice@example.com"}, headersUserA)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("non-admin assign owner status = %d, want 403", resp.StatusCode)
	}

	// Ingest sample records to Project B
	ingestRecords(t, server.URL, projectB.ProjectID, projectB.APIKey, sampleRecords())

	// -------------------------------------------------------------------
	// Section 2: User A CANNOT access Project B
	// -------------------------------------------------------------------

	// 2a. List: User A's list must not reveal Project B
	resp, body = doJSON(t, http.MethodGet, server.URL+"/api/projects", nil, headersUserA)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("User A list status = %d", resp.StatusCode)
	}
	if strings.Contains(string(body), projectB.ProjectID) {
		t.Fatalf("User A list should NOT contain Project B: %s", body)
	}

	// 2b. Get Project: 404
	resp, _ = doJSON(t, http.MethodGet, server.URL+"/api/projects/"+projectB.ProjectID, nil, headersUserA)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("User A get project status = %d, want 404", resp.StatusCode)
	}

	// 2c. Get Metrics: 404
	resp, _ = doJSON(t, http.MethodGet, server.URL+"/api/projects/"+projectB.ProjectID+"/metrics", nil, headersUserA)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("User A get metrics status = %d, want 404", resp.StatusCode)
	}

	// 2d. Get Anomalies: 404
	resp, _ = doJSON(t, http.MethodGet, server.URL+"/api/projects/"+projectB.ProjectID+"/anomalies", nil, headersUserA)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("User A get anomalies status = %d, want 404", resp.StatusCode)
	}

	// 2e. Get Thresholds: 404
	resp, _ = doJSON(t, http.MethodGet, server.URL+"/api/projects/"+projectB.ProjectID+"/thresholds", nil, headersUserA)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("User A get thresholds status = %d, want 404", resp.StatusCode)
	}

	// 2f. Put Thresholds: 403 (non-admin blocked at route layer before project lookup)
	resp, _ = doJSON(t, http.MethodPut, server.URL+"/api/projects/"+projectB.ProjectID+"/thresholds", updatedThreshold, headersUserA)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("User A update thresholds status = %d, want 403", resp.StatusCode)
	}

	// 2g. WebSocket: 404 for User A
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws/" + projectB.ProjectID
	wsHeadersUserA := http.Header{"X-Auth-Request-Email": []string{"alice@example.com"}}
	_, wsRespA, wsErrA := websocket.DefaultDialer.Dial(wsURL, wsHeadersUserA)
	if wsErrA == nil {
		t.Fatal("expected User A websocket dial to fail")
	}
	if wsRespA == nil || wsRespA.StatusCode != http.StatusNotFound {
		t.Fatalf("User A websocket status = %v, want 404", wsRespA)
	}

	// -------------------------------------------------------------------
	// Section 3: User B (owner, non-admin) can read Project B but NOT write thresholds
	// -------------------------------------------------------------------

	// 3a. List: User B sees Project B
	resp, body = doJSON(t, http.MethodGet, server.URL+"/api/projects", nil, headersUserB)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), projectB.ProjectID) {
		t.Fatalf("User B list status = %d, body = %s", resp.StatusCode, body)
	}

	// 3b. Get Project: 200
	resp, _ = doJSON(t, http.MethodGet, server.URL+"/api/projects/"+projectB.ProjectID, nil, headersUserB)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("User B get project status = %d, want 200", resp.StatusCode)
	}

	// 3c. Get Metrics: 200
	resp, _ = doJSON(t, http.MethodGet, server.URL+"/api/projects/"+projectB.ProjectID+"/metrics", nil, headersUserB)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("User B get metrics status = %d, want 200", resp.StatusCode)
	}

	// 3d. Get Anomalies: 200
	resp, _ = doJSON(t, http.MethodGet, server.URL+"/api/projects/"+projectB.ProjectID+"/anomalies", nil, headersUserB)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("User B get anomalies status = %d, want 200", resp.StatusCode)
	}

	// 3e. Get Thresholds: 200
	resp, _ = doJSON(t, http.MethodGet, server.URL+"/api/projects/"+projectB.ProjectID+"/thresholds", nil, headersUserB)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("User B get thresholds status = %d, want 200", resp.StatusCode)
	}

	// 3f. Put Thresholds: 403 (threshold writes remain admin-only at the route level)
	resp, _ = doJSON(t, http.MethodPut, server.URL+"/api/projects/"+projectB.ProjectID+"/thresholds", updatedThreshold, headersUserB)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("User B update thresholds status = %d, want 403", resp.StatusCode)
	}

	// 3g. WebSocket: User B connects and receives initial snapshot
	wsHeadersUserB := http.Header{"X-Auth-Request-Email": []string{"bob@example.com"}}
	connB, _, wsErrB := websocket.DefaultDialer.Dial(wsURL, wsHeadersUserB)
	if wsErrB != nil {
		t.Fatalf("User B websocket dial: %v", wsErrB)
	}
	_ = connB.SetReadDeadline(time.Now().Add(2 * time.Second))
	var envelope struct {
		EventType string          `json:"event_type"`
		ProjectID string          `json:"project_id"`
		Data      domain.Snapshot `json:"data"`
	}
	if err := connB.ReadJSON(&envelope); err != nil {
		t.Fatalf("User B read websocket: %v", err)
	}
	if envelope.ProjectID != projectB.ProjectID {
		t.Fatalf("User B websocket project id = %q, want %q", envelope.ProjectID, projectB.ProjectID)
	}
	connB.Close()

	// -------------------------------------------------------------------
	// Section 4: Admin can access and write to Project B
	// -------------------------------------------------------------------

	// 4a. List: Admin sees Project B
	resp, body = doJSON(t, http.MethodGet, server.URL+"/api/projects", nil, headersAdmin)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), projectB.ProjectID) {
		t.Fatalf("Admin list status = %d, body = %s", resp.StatusCode, body)
	}

	// 4b. Get Project: 200
	resp, _ = doJSON(t, http.MethodGet, server.URL+"/api/projects/"+projectB.ProjectID, nil, headersAdmin)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Admin get project status = %d, want 200", resp.StatusCode)
	}

	// 4c. Get Metrics: 200
	resp, _ = doJSON(t, http.MethodGet, server.URL+"/api/projects/"+projectB.ProjectID+"/metrics", nil, headersAdmin)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Admin get metrics status = %d, want 200", resp.StatusCode)
	}

	// 4d. Update Thresholds: 200 (admin-only write succeeds)
	resp, _ = doJSON(t, http.MethodPut, server.URL+"/api/projects/"+projectB.ProjectID+"/thresholds", updatedThreshold, headersAdmin)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Admin update thresholds status = %d, want 200", resp.StatusCode)
	}

	// 4e. WebSocket: Admin connects and receives snapshot
	wsHeadersAdmin := http.Header{"X-Auth-Request-Email": []string{"admin@example.com"}}
	connAdmin, _, wsErrAdmin := websocket.DefaultDialer.Dial(wsURL, wsHeadersAdmin)
	if wsErrAdmin != nil {
		t.Fatalf("Admin websocket dial: %v", wsErrAdmin)
	}
	_ = connAdmin.SetReadDeadline(time.Now().Add(2 * time.Second))
	if err := connAdmin.ReadJSON(&envelope); err != nil {
		t.Fatalf("Admin read websocket: %v", err)
	}
	connAdmin.Close()

	// -------------------------------------------------------------------
	// Section 5: Legacy project (owner_email = "") — admin-only
	// -------------------------------------------------------------------
	if _, err := helper.DB.Exec(
		`INSERT INTO projects (id, name, api_key_hash, created_at, owner_email) VALUES ('legacy1', 'Legacy Project', 'hash', ?, '')`,
		time.Now().UnixMilli(),
	); err != nil {
		t.Fatalf("insert legacy project: %v", err)
	}

	// Non-admin (User A) gets 404 for legacy project
	resp, _ = doJSON(t, http.MethodGet, server.URL+"/api/projects/legacy1", nil, headersUserA)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("User A get legacy project status = %d, want 404", resp.StatusCode)
	}
	// Non-admin list does not include legacy project
	resp, body = doJSON(t, http.MethodGet, server.URL+"/api/projects", nil, headersUserA)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("User A list status = %d, body = %s", resp.StatusCode, body)
	}
	if strings.Contains(string(body), "legacy1") {
		t.Fatalf("User A list should NOT contain legacy1: %s", body)
	}

	// Admin gets 200 for legacy project
	resp, _ = doJSON(t, http.MethodGet, server.URL+"/api/projects/legacy1", nil, headersAdmin)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Admin get legacy project status = %d, want 200", resp.StatusCode)
	}
	// Admin list includes legacy project
	resp, body = doJSON(t, http.MethodGet, server.URL+"/api/projects", nil, headersAdmin)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Admin list status = %d, body = %s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "legacy1") {
		t.Fatalf("Admin list should contain legacy1: %s", body)
	}

	// -------------------------------------------------------------------
	// Section 6: Every cross-tenant REST denial is audit-logged, with the same
	// 404 the caller already saw.
	// -------------------------------------------------------------------
	denials := listAuditQuery(t, server, headersAdmin,
		"action="+domain.AuditAccessDenied+"&project_id="+projectB.ProjectID)
	if len(denials) != 5 {
		t.Fatalf("access.denied events = %d, want 5 (project, metrics, anomalies, thresholds, websocket): %+v",
			len(denials), denials)
	}
	for _, event := range denials {
		if event.ActorEmail != "alice@example.com" {
			t.Fatalf("denial actor = %q, want alice@example.com", event.ActorEmail)
		}
		if event.Outcome != domain.AuditOutcomeFailure {
			t.Fatalf("denial outcome = %q, want failure", event.Outcome)
		}
	}
}

func ingestedOK(t *testing.T, baseURL, projectID, apiKey string) *http.Response {
	t.Helper()
	response, _ := doJSON(t, http.MethodPost, baseURL+"/ingest/"+projectID,
		map[string]any{"logs": sampleRecords()},
		map[string]string{"X-API-Key": apiKey},
	)
	return response
}

func TestAPIKeyRotation(t *testing.T) {
	server, _ := newProxyTestServer(t, []string{"admin@example.com"})
	headersAdmin := map[string]string{"X-Auth-Request-Email": "admin@example.com"}

	resp, body := doJSON(t, http.MethodPost, server.URL+"/api/projects", map[string]string{"name": "Rotate Me"}, headersAdmin)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create project status = %d, body = %s", resp.StatusCode, body)
	}
	var project createdProject
	if err := json.Unmarshal(body, &project); err != nil {
		t.Fatalf("decode project: %v", err)
	}

	// The original key works.
	if status := ingestedOK(t, server.URL, project.ProjectID, project.APIKey); status.StatusCode != http.StatusAccepted {
		t.Fatalf("ingest with original key status = %d", status.StatusCode)
	}

	// Rotate: the new key is returned exactly once.
	resp, body = doJSON(t, http.MethodPost, server.URL+"/api/projects/"+project.ProjectID+"/api-key", nil, headersAdmin)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("rotate status = %d, body = %s", resp.StatusCode, body)
	}
	var rotated createdProject
	if err := json.Unmarshal(body, &rotated); err != nil {
		t.Fatalf("decode rotated key: %v", err)
	}
	if rotated.ProjectID != project.ProjectID || rotated.APIKey == "" {
		t.Fatalf("incomplete rotation response: %+v", rotated)
	}
	if rotated.APIKey == project.APIKey {
		t.Fatal("rotated key must differ from the previous key")
	}

	// The previous key is invalidated; the new one works.
	if status := ingestedOK(t, server.URL, project.ProjectID, project.APIKey); status.StatusCode != http.StatusUnauthorized {
		t.Fatalf("ingest with old key after rotation status = %d, want 401", status.StatusCode)
	}
	if status := ingestedOK(t, server.URL, project.ProjectID, rotated.APIKey); status.StatusCode != http.StatusAccepted {
		t.Fatalf("ingest with rotated key status = %d, want 202", status.StatusCode)
	}

	// The plaintext key is never returned by reads.
	_, body = doJSON(t, http.MethodGet, server.URL+"/api/projects/"+project.ProjectID, nil, headersAdmin)
	if strings.Contains(string(body), rotated.APIKey) {
		t.Fatal("plaintext key leaked on project read")
	}

	// Rotating an unknown project is a 404.
	resp, _ = doJSON(t, http.MethodPost, server.URL+"/api/projects/missing/api-key", nil, headersAdmin)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("rotate unknown project status = %d, want 404", resp.StatusCode)
	}
}

func TestAPIKeyRevocation(t *testing.T) {
	server, _ := newProxyTestServer(t, []string{"admin@example.com"})
	headersAdmin := map[string]string{"X-Auth-Request-Email": "admin@example.com"}

	resp, body := doJSON(t, http.MethodPost, server.URL+"/api/projects", map[string]string{"name": "Revoke Me"}, headersAdmin)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create project status = %d, body = %s", resp.StatusCode, body)
	}
	var project createdProject
	if err := json.Unmarshal(body, &project); err != nil {
		t.Fatalf("decode project: %v", err)
	}

	resp, _ = doJSON(t, http.MethodDelete, server.URL+"/api/projects/"+project.ProjectID+"/api-key", nil, headersAdmin)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("revoke status = %d, want 204", resp.StatusCode)
	}

	// A revoked key can no longer ingest traffic.
	if status := ingestedOK(t, server.URL, project.ProjectID, project.APIKey); status.StatusCode != http.StatusUnauthorized {
		t.Fatalf("ingest with revoked key status = %d, want 401", status.StatusCode)
	}

	// Rotating after revocation re-activates ingest.
	resp, body = doJSON(t, http.MethodPost, server.URL+"/api/projects/"+project.ProjectID+"/api-key", nil, headersAdmin)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("rotate after revoke status = %d, body = %s", resp.StatusCode, body)
	}
	var rotated createdProject
	if err := json.Unmarshal(body, &rotated); err != nil {
		t.Fatalf("decode rotated key: %v", err)
	}
	if status := ingestedOK(t, server.URL, project.ProjectID, rotated.APIKey); status.StatusCode != http.StatusAccepted {
		t.Fatalf("ingest after rotate-post-revoke status = %d, want 202", status.StatusCode)
	}
}

func TestProjectLifecycleMutationsRequireAdmin(t *testing.T) {
	server, _ := newProxyTestServer(t, []string{"admin@example.com"})
	headersAdmin := map[string]string{"X-Auth-Request-Email": "admin@example.com"}
	headersUserA := map[string]string{"X-Auth-Request-Email": "alice@example.com"}

	resp, body := doJSON(t, http.MethodPost, server.URL+"/api/projects", map[string]string{"name": "Guarded"}, headersAdmin)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create project status = %d, body = %s", resp.StatusCode, body)
	}
	var project createdProject
	if err := json.Unmarshal(body, &project); err != nil {
		t.Fatalf("decode project: %v", err)
	}

	// Non-admin key rotation, revocation, and deletion are forbidden.
	resp, _ = doJSON(t, http.MethodPost, server.URL+"/api/projects/"+project.ProjectID+"/api-key", nil, headersUserA)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("non-admin rotate status = %d, want 403", resp.StatusCode)
	}
	resp, _ = doJSON(t, http.MethodDelete, server.URL+"/api/projects/"+project.ProjectID+"/api-key", nil, headersUserA)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("non-admin revoke status = %d, want 403", resp.StatusCode)
	}
	resp, _ = doJSON(t, http.MethodDelete, server.URL+"/api/projects/"+project.ProjectID, nil, headersUserA)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("non-admin delete status = %d, want 403", resp.StatusCode)
	}

	// Requests without any identity are unauthorized.
	resp, _ = doJSON(t, http.MethodDelete, server.URL+"/api/projects/"+project.ProjectID, nil, nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous delete status = %d, want 401", resp.StatusCode)
	}
}

func TestProjectDeletion(t *testing.T) {
	server, _ := newProxyTestServer(t, []string{"admin@example.com"})
	headersAdmin := map[string]string{"X-Auth-Request-Email": "admin@example.com"}

	resp, body := doJSON(t, http.MethodPost, server.URL+"/api/projects", map[string]string{"name": "Doomed"}, headersAdmin)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create project status = %d, body = %s", resp.StatusCode, body)
	}
	var project createdProject
	if err := json.Unmarshal(body, &project); err != nil {
		t.Fatalf("decode project: %v", err)
	}

	ingestRecords(t, server.URL, project.ProjectID, project.APIKey, sampleRecords())

	// Delete the project.
	resp, _ = doJSON(t, http.MethodDelete, server.URL+"/api/projects/"+project.ProjectID, nil, headersAdmin)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete status = %d, want 204", resp.StatusCode)
	}

	// Every read surface reports not found consistently.
	for _, path := range []string{
		"/api/projects/" + project.ProjectID,
		"/api/projects/" + project.ProjectID + "/metrics",
		"/api/projects/" + project.ProjectID + "/anomalies",
		"/api/projects/" + project.ProjectID + "/thresholds",
	} {
		resp, _ = doJSON(t, http.MethodGet, server.URL+path, nil, headersAdmin)
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("GET %s after delete status = %d, want 404", path, resp.StatusCode)
		}
	}

	// The deleted project cannot ingest traffic and is not listed.
	if status := ingestedOK(t, server.URL, project.ProjectID, project.APIKey); status.StatusCode != http.StatusUnauthorized {
		t.Fatalf("ingest after delete status = %d, want 401", status.StatusCode)
	}
	resp, body = doJSON(t, http.MethodGet, server.URL+"/api/projects", nil, headersAdmin)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list after delete status = %d, body = %s", resp.StatusCode, body)
	}
	if strings.Contains(string(body), project.ProjectID) {
		t.Fatalf("deleted project still listed: %s", body)
	}

	// New WebSocket connections are refused with a consistent 404.
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws/" + project.ProjectID
	wsHeaders := http.Header{"X-Auth-Request-Email": []string{"admin@example.com"}}
	_, wsResp, wsErr := websocket.DefaultDialer.Dial(wsURL, wsHeaders)
	if wsErr == nil {
		t.Fatal("expected websocket dial on deleted project to fail")
	}
	if wsResp == nil || wsResp.StatusCode != http.StatusNotFound {
		t.Fatalf("websocket status = %v, want 404", wsResp)
	}

	// Deleting twice reports not found.
	resp, _ = doJSON(t, http.MethodDelete, server.URL+"/api/projects/"+project.ProjectID, nil, headersAdmin)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("second delete status = %d, want 404", resp.StatusCode)
	}
}

func listAudit(t *testing.T, server *httptest.Server, headers map[string]string) []domain.AuditEvent {
	t.Helper()
	return listAuditQuery(t, server, headers, "")
}

func listAuditQuery(t *testing.T, server *httptest.Server, headers map[string]string, rawQuery string) []domain.AuditEvent {
	t.Helper()

	path := server.URL + "/api/audit?limit=1000"
	if rawQuery != "" {
		path += "&" + rawQuery
	}
	response, body := doJSON(t, http.MethodGet, path, nil, headers)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("audit list status = %d, body = %s", response.StatusCode, body)
	}
	var events []domain.AuditEvent
	if err := json.Unmarshal(body, &events); err != nil {
		t.Fatalf("decode audit events: %v", err)
	}
	return events
}

func TestAuditRecordsSecurityEvents(t *testing.T) {
	server, _ := newProxyTestServer(t, []string{"admin@example.com"})
	headersAdmin := map[string]string{"X-Auth-Request-Email": "admin@example.com"}

	resp, body := doJSON(t, http.MethodPost, server.URL+"/api/projects", map[string]string{"name": "Audited"}, headersAdmin)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create project status = %d, body = %s", resp.StatusCode, body)
	}
	var project createdProject
	if err := json.Unmarshal(body, &project); err != nil {
		t.Fatalf("decode project: %v", err)
	}

	updated := domain.Threshold{P95Ms: 250, ErrorRate: 2, RPSMax: 80, ReqPerIPMax: 10}
	if resp, _ := doJSON(t, http.MethodPut, server.URL+"/api/projects/"+project.ProjectID+"/thresholds", updated, headersAdmin); resp.StatusCode != http.StatusOK {
		t.Fatalf("update thresholds status = %d", resp.StatusCode)
	}
	if resp, _ := doJSON(t, http.MethodPost, server.URL+"/api/projects/"+project.ProjectID+"/api-key", nil, headersAdmin); resp.StatusCode != http.StatusOK {
		t.Fatalf("rotate status = %d", resp.StatusCode)
	}
	if resp, _ := doJSON(t, http.MethodDelete, server.URL+"/api/projects/"+project.ProjectID+"/api-key", nil, headersAdmin); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("revoke status = %d", resp.StatusCode)
	}
	if resp, _ := doJSON(t, http.MethodDelete, server.URL+"/api/projects/"+project.ProjectID, nil, headersAdmin); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete status = %d", resp.StatusCode)
	}

	events := listAudit(t, server, headersAdmin)
	if len(events) != 5 {
		t.Fatalf("audit events = %d, want 5: %+v", len(events), events)
	}

	// Newest first: the delete is the most recent record.
	if events[0].Action != domain.AuditProjectDelete {
		t.Fatalf("newest action = %q, want %q", events[0].Action, domain.AuditProjectDelete)
	}

	expected := map[string]string{
		domain.AuditProjectCreate:   project.ProjectID,
		domain.AuditThresholdUpdate: project.ProjectID,
		domain.AuditKeyRotate:       project.ProjectID,
		domain.AuditKeyRevoke:       project.ProjectID,
		domain.AuditProjectDelete:   project.ProjectID,
	}
	for _, event := range events {
		if event.Outcome != domain.AuditOutcomeSuccess {
			t.Fatalf("action %q outcome = %q, want success", event.Action, event.Outcome)
		}
		if event.ActorEmail != "admin@example.com" {
			t.Fatalf("action %q actor = %q, want admin@example.com", event.Action, event.ActorEmail)
		}
		if event.TargetProjectID != project.ProjectID {
			t.Fatalf("action %q target = %q, want %q", event.Action, event.TargetProjectID, project.ProjectID)
		}
		if event.RequestID == "" {
			t.Fatalf("action %q request id empty", event.Action)
		}
		if event.Ts.IsZero() || event.Ts.Location() != time.UTC {
			t.Fatalf("action %q ts = %v, want UTC", event.Action, event.Ts)
		}
		if _, ok := expected[event.Action]; !ok {
			t.Fatalf("unexpected action %q", event.Action)
		}
		delete(expected, event.Action)
	}
	if len(expected) != 0 {
		t.Fatalf("missing audit actions: %v", expected)
	}
}

func TestAuditDeniedActionsAndAccessControl(t *testing.T) {
	server, _ := newProxyTestServer(t, []string{"admin@example.com"})
	headersAdmin := map[string]string{"X-Auth-Request-Email": "admin@example.com"}
	headersUserA := map[string]string{"X-Auth-Request-Email": "alice@example.com"}
	stolenKey := "sk-live-0000000000000000000000000000deadbeef"

	resp, body := doJSON(t, http.MethodPost, server.URL+"/api/projects", map[string]string{"name": "Target"}, headersAdmin)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create project status = %d, body = %s", resp.StatusCode, body)
	}
	var project createdProject
	if err := json.Unmarshal(body, &project); err != nil {
		t.Fatalf("decode project: %v", err)
	}

	// Non-admin create is denied and audit-logged.
	resp, _ = doJSON(t, http.MethodPost, server.URL+"/api/projects", map[string]string{"name": "Sneaky"}, headersUserA)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("non-admin create status = %d, want 403", resp.StatusCode)
	}

	// Failed machine authentication is audit-logged.
	if status := ingestedOK(t, server.URL, project.ProjectID, stolenKey); status.StatusCode != http.StatusUnauthorized {
		t.Fatalf("ingest with stolen key status = %d, want 401", status.StatusCode)
	}

	// The audit feed itself is protected: anonymous 401, non-admin 403.
	resp, _ = doJSON(t, http.MethodGet, server.URL+"/api/audit", nil, nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous audit read status = %d, want 401", resp.StatusCode)
	}
	resp, _ = doJSON(t, http.MethodGet, server.URL+"/api/audit", nil, headersUserA)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("non-admin audit read status = %d, want 403", resp.StatusCode)
	}

	// Only the admin sees the trail.
	events := listAudit(t, server, headersAdmin)
	byAction := map[string][]domain.AuditEvent{}
	for _, event := range events {
		byAction[event.Action] = append(byAction[event.Action], event)
	}

	if len(byAction[domain.AuditAdminDenied]) != 2 {
		t.Fatalf("admin.denied events = %d, want 2 (create + audit read): %+v",
			len(byAction[domain.AuditAdminDenied]), byAction[domain.AuditAdminDenied])
	}
	for _, event := range byAction[domain.AuditAdminDenied] {
		if event.ActorEmail != "alice@example.com" || event.Outcome != domain.AuditOutcomeFailure {
			t.Fatalf("admin.denied = %+v, want actor alice with failure outcome", event)
		}
		if event.RequestID == "" || event.Details["reason"] == "" {
			t.Fatalf("admin.denied missing correlation data: %+v", event)
		}
	}

	if len(byAction[domain.AuditAuthDenied]) != 1 {
		t.Fatalf("auth.denied events = %d, want 1: %+v", len(byAction[domain.AuditAuthDenied]), byAction[domain.AuditAuthDenied])
	}
	if got := byAction[domain.AuditAuthDenied][0]; got.Outcome != domain.AuditOutcomeFailure || got.Details["reason"] == "" {
		t.Fatalf("auth.denied = %+v, want failure with reason", got)
	}

	if len(byAction[domain.AuditIngestDenied]) != 1 {
		t.Fatalf("ingest.denied events = %d, want 1: %+v", len(byAction[domain.AuditIngestDenied]), byAction[domain.AuditIngestDenied])
	}
	ingestDenied := byAction[domain.AuditIngestDenied][0]
	if ingestDenied.TargetProjectID != project.ProjectID || ingestDenied.Outcome != domain.AuditOutcomeFailure {
		t.Fatalf("ingest.denied = %+v, want failure targeting the project", ingestDenied)
	}

	// The presented key must never appear anywhere in the audit trail.
	encoded, err := json.Marshal(events)
	if err != nil {
		t.Fatalf("encode audit events: %v", err)
	}
	if strings.Contains(string(encoded), stolenKey) {
		t.Fatal("audit trail leaked the presented API key")
	}
}

func TestAuditNeverContainsRotatedPlaintextKey(t *testing.T) {
	server, _ := newProxyTestServer(t, []string{"admin@example.com"})
	headersAdmin := map[string]string{"X-Auth-Request-Email": "admin@example.com"}

	resp, body := doJSON(t, http.MethodPost, server.URL+"/api/projects", map[string]string{"name": "Leak Check"}, headersAdmin)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create project status = %d, body = %s", resp.StatusCode, body)
	}
	var project createdProject
	if err := json.Unmarshal(body, &project); err != nil {
		t.Fatalf("decode project: %v", err)
	}

	resp, body = doJSON(t, http.MethodPost, server.URL+"/api/projects/"+project.ProjectID+"/api-key", nil, headersAdmin)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("rotate status = %d, body = %s", resp.StatusCode, body)
	}
	var rotated createdProject
	if err := json.Unmarshal(body, &rotated); err != nil {
		t.Fatalf("decode rotated key: %v", err)
	}

	events := listAudit(t, server, headersAdmin)
	encoded, err := json.Marshal(events)
	if err != nil {
		t.Fatalf("encode audit events: %v", err)
	}
	if strings.Contains(string(encoded), rotated.APIKey) || strings.Contains(string(encoded), project.APIKey) {
		t.Fatal("audit trail leaked a plaintext API key")
	}

	// Details are curated: rotation carries no key-shaped fields.
	for _, event := range events {
		for key := range event.Details {
			lowered := strings.ToLower(key)
			if strings.Contains(lowered, "key") || strings.Contains(lowered, "secret") || strings.Contains(lowered, "token") {
				t.Fatalf("audit action %q has sensitive detail field %q", event.Action, key)
			}
		}
	}
}

func TestAuditRecordsWebSocketAccessDenial(t *testing.T) {
	server, _ := newProxyTestServer(t, []string{"admin@example.com"})
	headersAdmin := map[string]string{"X-Auth-Request-Email": "admin@example.com"}

	// The admin owns the project; alice has no access to it.
	resp, body := doJSON(t, http.MethodPost, server.URL+"/api/projects", map[string]string{"name": "Private"}, headersAdmin)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create project status = %d, body = %s", resp.StatusCode, body)
	}
	var project createdProject
	if err := json.Unmarshal(body, &project); err != nil {
		t.Fatalf("decode project: %v", err)
	}

	// Alice dials the WebSocket of a project she cannot access. The dial must
	// fail with 404 and the response must conceal the project's existence.
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws/" + project.ProjectID
	wsHeaders := http.Header{"X-Auth-Request-Email": []string{"alice@example.com"}}
	_, wsResp, wsErr := websocket.DefaultDialer.Dial(wsURL, wsHeaders)
	if wsErr == nil {
		t.Fatal("expected unauthorized websocket dial to fail")
	}
	if wsResp == nil || wsResp.StatusCode != http.StatusNotFound {
		t.Fatalf("unauthorized websocket status = %v, want 404", wsResp)
	}
	denialBody, readErr := io.ReadAll(wsResp.Body)
	if readErr != nil {
		t.Fatalf("read denial response body: %v", readErr)
	}
	wsResp.Body.Close()
	if strings.Contains(string(denialBody), project.ProjectID) {
		t.Fatalf("denial response leaked the project id: %s", denialBody)
	}

	// The denial is recorded with full correlation data for the admin.
	events := listAudit(t, server, headersAdmin)
	denials := []domain.AuditEvent{}
	for _, event := range events {
		if event.Action == domain.AuditAccessDenied {
			denials = append(denials, event)
		}
	}
	if len(denials) != 1 {
		t.Fatalf("access.denied events = %d, want 1: %+v", len(denials), denials)
	}
	denied := denials[0]
	if denied.ActorEmail != "alice@example.com" {
		t.Fatalf("access.denied actor = %q, want alice@example.com", denied.ActorEmail)
	}
	if denied.TargetProjectID != project.ProjectID {
		t.Fatalf("access.denied target = %q, want %q", denied.TargetProjectID, project.ProjectID)
	}
	if denied.Outcome != domain.AuditOutcomeFailure {
		t.Fatalf("access.denied outcome = %q, want failure", denied.Outcome)
	}
	if denied.RequestID == "" || denied.Details["reason"] == "" {
		t.Fatalf("access.denied missing correlation data: %+v", denied)
	}
	if denied.Ts.IsZero() || denied.Ts.Location() != time.UTC {
		t.Fatalf("access.denied ts = %v, want UTC", denied.Ts)
	}

	// The owner (admin) dials the same WebSocket successfully: no denial event.
	upgradeConn, _, upgradeErr := websocket.DefaultDialer.Dial(wsURL, http.Header{"X-Auth-Request-Email": []string{"admin@example.com"}})
	if upgradeErr != nil {
		t.Fatalf("admin websocket dial: %v", upgradeErr)
	}
	defer upgradeConn.Close()

	// The owner's successful connection must not add any further denial.
	events = listAudit(t, server, headersAdmin)
	denials = denials[:0]
	for _, event := range events {
		if event.Action == domain.AuditAccessDenied {
			denials = append(denials, event)
		}
	}
	if len(denials) != 1 {
		t.Fatalf("access.denied events after owner connect = %d, want still 1: %+v", len(denials), denials)
	}
}

func createProjectAs(t *testing.T, baseURL string, headers map[string]string, name string) createdProject {
	t.Helper()

	resp, body := doJSON(t, http.MethodPost, baseURL+"/api/projects", map[string]string{"name": name}, headers)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create project status = %d, body = %s", resp.StatusCode, body)
	}
	var project createdProject
	if err := json.Unmarshal(body, &project); err != nil {
		t.Fatalf("decode project: %v", err)
	}
	return project
}

func TestProjectDeletionClosesLiveStream(t *testing.T) {
	server, _ := newProxyTestServer(t, []string{"admin@example.com"})
	headersAdmin := map[string]string{"X-Auth-Request-Email": "admin@example.com"}

	project := createProjectAs(t, server.URL, headersAdmin, "Stream Doomed")

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws/" + project.ProjectID
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, http.Header{"X-Auth-Request-Email": []string{"admin@example.com"}})
	if err != nil {
		t.Fatalf("websocket dial: %v", err)
	}
	defer conn.Close()

	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	var envelope struct {
		EventType string `json:"event_type"`
	}
	if err := conn.ReadJSON(&envelope); err != nil {
		t.Fatalf("read initial snapshot: %v", err)
	}

	resp, body := doJSON(t, http.MethodDelete, server.URL+"/api/projects/"+project.ProjectID, nil, headersAdmin)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete status = %d, body = %s", resp.StatusCode, body)
	}

	// The hub drops the project's subscribers, so the open stream ends instead
	// of staying silent forever.
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, _, err := conn.ReadMessage(); err == nil {
		t.Fatal("expected the live stream to close after project deletion")
	}
}

func TestAuditFeedFiltersAndCursor(t *testing.T) {
	server, _ := newProxyTestServer(t, []string{"admin@example.com"})
	headersAdmin := map[string]string{"X-Auth-Request-Email": "admin@example.com"}
	headersUserA := map[string]string{"X-Auth-Request-Email": "alice@example.com"}

	project := createProjectAs(t, server.URL, headersAdmin, "Audit Filter")

	// Distinct millisecond timestamps keep the cursor assertion deterministic:
	// SQLite stores audit timestamps as unix milliseconds.
	time.Sleep(3 * time.Millisecond)
	if resp, _ := doJSON(t, http.MethodPost, server.URL+"/api/projects/"+project.ProjectID+"/api-key", nil, headersAdmin); resp.StatusCode != http.StatusOK {
		t.Fatalf("rotate status = %d", resp.StatusCode)
	}
	time.Sleep(3 * time.Millisecond)
	if resp, _ := doJSON(t, http.MethodDelete, server.URL+"/api/projects/"+project.ProjectID+"/api-key", nil, headersAdmin); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("revoke status = %d", resp.StatusCode)
	}

	all := listAudit(t, server, headersAdmin)
	if len(all) != 3 {
		t.Fatalf("audit events = %d, want 3: %+v", len(all), all)
	}
	if all[0].Action != domain.AuditKeyRevoke || all[2].Action != domain.AuditProjectCreate {
		t.Fatalf("unexpected order: %+v", all)
	}

	byAction := listAuditQuery(t, server, headersAdmin, "action="+domain.AuditKeyRotate)
	if len(byAction) != 1 || byAction[0].Action != domain.AuditKeyRotate {
		t.Fatalf("action filter = %+v, want one rotate", byAction)
	}

	unknownAction := listAuditQuery(t, server, headersAdmin, "action=does.not.exist")
	if len(unknownAction) != 0 {
		t.Fatalf("unknown action returned %d events, want 0", len(unknownAction))
	}

	byProject := listAuditQuery(t, server, headersAdmin, "project_id="+project.ProjectID)
	if len(byProject) != 3 {
		t.Fatalf("project filter = %d events, want 3", len(byProject))
	}

	cursor := url.Values{}
	cursor.Set("before", all[1].Ts.Format(time.RFC3339Nano))
	cursor.Set("before_id", strconv.FormatInt(all[1].ID, 10))
	older := listAuditQuery(t, server, headersAdmin, cursor.Encode())
	if len(older) != 1 || older[0].Action != domain.AuditProjectCreate {
		t.Fatalf("cursor page = %+v, want only the create", older)
	}

	// A malformed cursor is a client error, not a silent full dump.
	resp, _ := doJSON(t, http.MethodGet, server.URL+"/api/audit?before=not-a-timestamp", nil, headersAdmin)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid cursor status = %d, want 400", resp.StatusCode)
	}

	// The feed stays admin-only.
	resp, _ = doJSON(t, http.MethodGet, server.URL+"/api/audit", nil, headersUserA)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("non-admin audit status = %d, want 403", resp.StatusCode)
	}
}
