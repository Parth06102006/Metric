package sqlite_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	anomalysqlite "traffic-center/anomaly/repository/sqlite"
	"traffic-center/domain"
	storesqlite "traffic-center/internal/sqlite"
)

func TestSaveWithAnalysisPersistsIncidentAndJobAtomically(t *testing.T) {
	db, err := storesqlite.Open(filepath.Join(t.TempDir(), "queue.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := anomalysqlite.New(db)
	a := domain.Anomaly{Type: "high_latency", Severity: "high", Message: "slow", Value: 20, Threshold: 10, DetectedAt: time.Now()}
	snapshot := domain.ComputeAt([]domain.LogRecord{{Endpoint: "/secret", IP: "192.0.2.1", Status: 500, LatencyMs: 100}}, 60, time.Now())
	if err := repo.SaveWithAnalysis(context.Background(), "p1", &a, snapshot, 1); err != nil {
		t.Fatal(err)
	}
	var state, status string
	if err := db.QueryRow(`SELECT state_json,status FROM analysis_jobs WHERE anomaly_id = ?`, a.ID).Scan(&state, &status); err != nil {
		t.Fatal(err)
	}
	if status != "pending" {
		t.Fatalf("job status %q", status)
	}
	if state == "" {
		t.Fatal("empty state")
	}
	if json.Valid([]byte(state)) == false {
		t.Fatal("invalid state")
	}
	if strings.Contains(state, "/secret") || strings.Contains(state, "192.0.2.1") {
		t.Fatal("sensitive path or IP in state")
	}
	var evidence domain.IncidentEvidence
	if err := json.Unmarshal([]byte(state), &evidence); err != nil {
		t.Fatal(err)
	}
	if evidence.RequestCount == nil || *evidence.RequestCount != 1 || evidence.Endpoints == nil || evidence.Endpoints.MostErrors != 1 {
		t.Fatalf("missing aggregate evidence: %+v", evidence)
	}
	a2 := domain.Anomaly{Type: "high_traffic", Severity: "high", Message: "busy", DetectedAt: time.Now()}
	if err := repo.SaveWithAnalysis(context.Background(), "p1", &a2, domain.Snapshot{}, 1); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT status FROM analysis_jobs WHERE anomaly_id = ?`, a2.ID).Scan(&status); err != sql.ErrNoRows {
		t.Fatalf("queue should be bounded, got %v", err)
	}
	if !strings.Contains(a2.Analysis, "skipped") {
		t.Fatalf("skipped status not visible in analysis: %q", a2.Analysis)
	}
}
