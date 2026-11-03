package server_test

import (
	"context"
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
	"traffic-center/audit"
	auditsqlite "traffic-center/audit/repository/sqlite"
	"traffic-center/domain"
	"traffic-center/ingest"
	"traffic-center/internal/analysis"
	"traffic-center/internal/config"
	"traffic-center/internal/jev"
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

// A blocking provider makes the pending-to-completed transition observable
// without a real API key, network service, sleeps, or provider charges.
type integrationAnalyst struct {
	called  chan jev.State
	release chan struct{}
}

func (a *integrationAnalyst) Diagnose(ctx context.Context, state jev.State) (jev.Result, error) {
	select {
	case a.called <- state:
	case <-ctx.Done():
		return jev.Result{}, ctx.Err()
	}
	select {
	case <-a.release:
		return jev.Result{Pattern: "error_burst", Direction: "more_evidence", Model: "integration-fake", EvidenceProbability: 0.5}, nil
	case <-ctx.Done():
		return jev.Result{}, ctx.Err()
	}
}

func TestIntegrationIngestIncidentAnalysisAndWebSocket(t *testing.T) {
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "integration.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	projects := projectsqlite.New(db)
	thresholds := thresholdsqlite.New(db)
	records := metricsmemory.New(time.Minute)
	snapshots := metricssqlite.New(db)
	incidents := anomalysqlite.New(db)
	hub := ws.NewHub()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	metricsUsecase := metrics.NewUsecase(records, snapshots, time.Minute)
	anomalyUsecase := anomaly.NewUsecase(incidents, thresholds, nil, hub, time.Minute)
	provider := &integrationAnalyst{called: make(chan jev.State, 1), release: make(chan struct{})}
	analyst := analysis.NewWorker(incidents, provider, snapshots)
	analyst.SetBroadcaster(hub)
	analyst.Start()
	t.Cleanup(analyst.Stop)
	anomalyUsecase.EnableAsyncAnalysis(100, analyst.Notify)
	snapshotWorker := anomaly.NewWorker(records, metricsUsecase, snapshots, anomalyUsecase, hub, time.Second, 0, logger)

	cfg := config.Config{AuthMode: "dev", DevEmail: "dev@test", AuthEmailHeader: "X-Auth-Request-Email", AuthUserHeader: "X-Auth-Request-User", MetricsWindow: time.Minute, APITimeout: 5 * time.Second}
	handler := server.New(cfg, logger, server.Deps{
		Projects: project.NewUsecase(projects), Lifecycle: project.NewLifecycle(projects, records, hub),
		Thresholds: threshold.NewUsecase(thresholds, projects), Metrics: metricsUsecase,
		Ingest: ingest.NewUsecase(projects, records), Anomalies: anomalyUsecase,
		Audit: audit.NewUsecase(auditsqlite.New(db), logger), ProjectRepo: projects, RecordRepo: records, Hub: hub,
	})
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	p := createProject(t, srv.URL, "Integration")

	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/"+p.ProjectID, nil)
	if err != nil {
		t.Fatalf("websocket dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	_ = conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	type event struct {
		EventType ws.EventType    `json:"event_type"`
		ProjectID string          `json:"project_id"`
		Data      json.RawMessage `json:"data"`
	}
	readEvent := func() event {
		t.Helper()
		var e event
		if err := conn.ReadJSON(&e); err != nil {
			t.Fatalf("read websocket event: %v", err)
		}
		if e.ProjectID != p.ProjectID {
			t.Fatalf("event for wrong project: %+v", e)
		}
		return e
	}
	if e := readEvent(); e.EventType != ws.EventMetricSnapshot {
		t.Fatalf("initial event: %+v", e)
	}

	logs := make([]domain.LogRecord, 20)
	for i := range logs {
		logs[i] = domain.LogRecord{Ts: time.Now().UTC(), Endpoint: "/private/checkout", Method: "GET", Status: 500, LatencyMs: 50, IP: "192.0.2.42"}
	}
	res, _ := doJSON(t, http.MethodPost, srv.URL+"/ingest/"+p.ProjectID, map[string]any{"logs": logs}, map[string]string{"X-API-Key": "wrong"})
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("invalid key status: %d", res.StatusCode)
	}
	ingestRecords(t, srv.URL, p.ProjectID, p.APIKey, logs)
	// Run the real snapshot/detection pipeline once, without depending on ticker timing.
	snapshotWorker.EvaluateProject(context.Background(), p.ProjectID)
	if e := readEvent(); e.EventType != ws.EventMetricSnapshot {
		t.Fatalf("snapshot event: %+v", e)
	}
	pendingEvent := readEvent()
	var pending domain.Anomaly
	if pendingEvent.EventType != ws.EventAnomalyAlert {
		t.Fatalf("pending event: %+v", pendingEvent)
	}
	if err := json.Unmarshal(pendingEvent.Data, &pending); err != nil {
		t.Fatal(err)
	}
	if pending.ID == 0 || pending.Type != "high_error_rate" || pending.AnalysisStatus != "pending" {
		t.Fatalf("pending incident: %+v", pending)
	}

	select {
	case state := <-provider.called:
		encoded, err := json.Marshal(state)
		if err != nil {
			t.Fatal(err)
		}
		if state.RequestCount == nil || *state.RequestCount != 20 || strings.Contains(string(encoded), "/private/checkout") || strings.Contains(string(encoded), "192.0.2.42") {
			t.Fatalf("provider evidence: %s", encoded)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("analysis worker did not invoke provider")
	}
	list := func() []domain.Anomaly {
		t.Helper()
		resp, body := doJSON(t, http.MethodGet, srv.URL+"/api/projects/"+p.ProjectID+"/anomalies", nil, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("list status %d: %s", resp.StatusCode, body)
		}
		var got []domain.Anomaly
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatal(err)
		}
		return got
	}
	if got := list(); len(got) != 1 || got[0].ID != pending.ID || got[0].AnalysisStatus != "pending" {
		t.Fatalf("persisted pending incident: %+v", got)
	}

	close(provider.release)
	completedEvent := readEvent()
	var completed domain.Anomaly
	if completedEvent.EventType != ws.EventAnomalyAlert {
		t.Fatalf("completed event: %+v", completedEvent)
	}
	if err := json.Unmarshal(completedEvent.Data, &completed); err != nil {
		t.Fatal(err)
	}
	if completed.ID != pending.ID || completed.AnalysisStatus != "completed" || completed.Diagnosis == nil || completed.Diagnosis.Model != "integration-fake" {
		t.Fatalf("completed incident: %+v", completed)
	}
	got := list()
	if len(got) != 1 || got[0].ID != pending.ID || got[0].AnalysisStatus != "completed" || got[0].Diagnosis == nil || got[0].Diagnosis.Evidence == nil || got[0].Diagnosis.Evidence.RequestCount == nil || *got[0].Diagnosis.Evidence.RequestCount != 20 {
		t.Fatalf("persisted completed incident: %+v", got)
	}
	var remaining int
	if err := db.QueryRow(`SELECT COUNT(*) FROM analysis_jobs WHERE anomaly_id = ?`, pending.ID).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("job not removed: %d, %v", remaining, err)
	}
}
