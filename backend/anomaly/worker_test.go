package anomaly

import (
	"path/filepath"
	"testing"
	"time"

	"traffic-center/backend/metrics"
	"traffic-center/backend/store"
)

type mockTracker struct {
	projects []string
	buffers  map[string]*metrics.Buffer
}

func (m *mockTracker) ActiveProjects() []string {
	return m.projects
}

func (m *mockTracker) GetBuffer(projectID string) (*metrics.Buffer, bool) {
	b, ok := m.buffers[projectID]
	return b, ok
}

func TestPipelineWorker_EvaluationAndDeduplication(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "worker_test.db")
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open failed: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	proj, _, err := s.CreateProject("Worker Project")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	// Set low thresholds so test traffic breaches them
	if err := s.UpdateThresholds(proj.ID, store.Thresholds{
		P95Ms:       100.0,
		ErrorRate:   10.0,
		RPSMax:      5.0,
		ReqPerIPMax: 5.0,
	}); err != nil {
		t.Fatalf("UpdateThresholds: %v", err)
	}

	buf := metrics.NewBuffer(60 * time.Second)
	// Add 10 records with 500 error status within the window
	for i := 0; i < 10; i++ {
		buf.Add(metrics.LogRecord{
			Ts:        time.Now(),
			Endpoint:  "/api/fail",
			Method:    "POST",
			Status:    500,
			LatencyMs: 150.0,
			IP:        "192.0.2.1",
		})
	}

	tracker := &mockTracker{
		projects: []string{proj.ID},
		buffers:  map[string]*metrics.Buffer{proj.ID: buf},
	}

	worker := NewWorker(s, tracker, nil, nil, 100*time.Millisecond)

	// Cycle 1: Evaluate
	worker.EvaluateProject(proj.ID)

	// Check that snapshot was saved
	var snapCount int
	if err := s.DB.QueryRow(`SELECT COUNT(1) FROM metric_snapshots WHERE project_id = ?`, proj.ID).Scan(&snapCount); err != nil {
		t.Fatalf("query snapshot count: %v", err)
	}
	if snapCount != 1 {
		t.Fatalf("expected 1 snapshot saved, got %d", snapCount)
	}

	// Check anomalies recorded
	var anomCount int
	if err := s.DB.QueryRow(`SELECT COUNT(1) FROM anomalies WHERE project_id = ?`, proj.ID).Scan(&anomCount); err != nil {
		t.Fatalf("query anomaly count: %v", err)
	}
	if anomCount == 0 {
		t.Fatalf("expected anomalies recorded, got 0")
	}

	firstAnomCount := anomCount

	// Cycle 2: Immediately evaluate again with same traffic
	// Since anomalies are ongoing within DedupeWindow (60s), NO new records should be inserted!
	worker.EvaluateProject(proj.ID)

	var secondAnomCount int
	if err := s.DB.QueryRow(`SELECT COUNT(1) FROM anomalies WHERE project_id = ?`, proj.ID).Scan(&secondAnomCount); err != nil {
		t.Fatalf("query anomaly count cycle 2: %v", err)
	}
	if secondAnomCount != firstAnomCount {
		t.Fatalf("deduplication failed: anomaly count increased from %d to %d on consecutive cycle", firstAnomCount, secondAnomCount)
	}

	// Snapshots DO continue to be recorded per cycle
	if err := s.DB.QueryRow(`SELECT COUNT(1) FROM metric_snapshots WHERE project_id = ?`, proj.ID).Scan(&snapCount); err != nil {
		t.Fatalf("query snapshot count cycle 2: %v", err)
	}
	if snapCount != 2 {
		t.Fatalf("expected 2 snapshots recorded across 2 cycles, got %d", snapCount)
	}
}

func TestPipelineWorker_StartStopSafe(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "worker_lifecycle.db")
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open failed: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	worker := NewWorker(s, &mockTracker{}, nil, nil, 20*time.Millisecond)
	worker.Start()
	time.Sleep(50 * time.Millisecond)
	worker.Stop() // Should not hang or panic
}
