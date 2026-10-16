package store

import (
	"path/filepath"
	"testing"
	"time"

	"traffic-center/backend/metrics"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	t.Cleanup(func() {
		s.Close()
	})
	return s
}

func TestStore_CreateAndAuthenticateProject(t *testing.T) {
	s := newTestStore(t)

	proj, apiKey, err := s.CreateProject("Production API")
	if err != nil {
		t.Fatalf("CreateProject failed: %v", err)
	}

	if proj.ID == "" || proj.Name != "Production API" || apiKey == "" {
		t.Fatalf("invalid project return: %+v, key=%s", proj, apiKey)
	}

	// Valid auth
	ok, err := s.AuthenticateProject(proj.ID, apiKey)
	if err != nil || !ok {
		t.Fatalf("expected successful auth, got ok=%v, err=%v", ok, err)
	}

	// Invalid auth
	ok, err = s.AuthenticateProject(proj.ID, "wrong-key")
	if err != nil || ok {
		t.Fatalf("expected auth failure with wrong key, got ok=%v, err=%v", ok, err)
	}
}

func TestStore_ThresholdsCRUD(t *testing.T) {
	s := newTestStore(t)
	proj, _, err := s.CreateProject("API with Thresholds")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	// Default thresholds
	defaults, err := s.GetThresholds(proj.ID)
	if err != nil {
		t.Fatalf("GetThresholds: %v", err)
	}
	if defaults.P95Ms != 1000 || defaults.ErrorRate != 5 || defaults.RPSMax != 100 || defaults.ReqPerIPMax != 30 {
		t.Fatalf("unexpected defaults: %+v", defaults)
	}

	// Update thresholds
	updated := Thresholds{
		P95Ms:       450.5,
		ErrorRate:   2.5,
		RPSMax:      250.0,
		ReqPerIPMax: 50.0,
	}
	if err := s.UpdateThresholds(proj.ID, updated); err != nil {
		t.Fatalf("UpdateThresholds: %v", err)
	}

	got, err := s.GetThresholds(proj.ID)
	if err != nil {
		t.Fatalf("GetThresholds after update: %v", err)
	}
	if got != updated {
		t.Fatalf("expected %+v, got %+v", updated, got)
	}
}

func TestStore_SaveSnapshotAndAnomaliesWithDeduplication(t *testing.T) {
	s := newTestStore(t)
	proj, _, err := s.CreateProject("Snapshot Project")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	snapshot := metrics.Snapshot{
		Ts:        time.Now(),
		WindowS:   60,
		RPS:       120.0,
		P95:       350.0,
		ErrorRate: 6.5,
	}

	// Test snapshot persistence
	if err := s.SaveSnapshot(proj.ID, snapshot); err != nil {
		t.Fatalf("SaveSnapshot failed: %v", err)
	}

	// Verify persistence in metric_snapshots
	var count int
	if err := s.DB.QueryRow(`SELECT COUNT(1) FROM metric_snapshots WHERE project_id = ?`, proj.ID).Scan(&count); err != nil {
		t.Fatalf("query snapshots failed: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 snapshot, got %d", count)
	}

	// Test anomaly recording
	anom := AnomalyRecord{
		Type:       "high_error_rate",
		Severity:   "critical",
		Message:    "Error rate 6.5% exceeds limit",
		Value:      6.5,
		Threshold:  5.0,
		DetectedAt: time.Now(),
	}

	// Initially no recent anomaly
	recent, err := s.HasRecentAnomaly(proj.ID, anom.Type, 30*time.Second)
	if err != nil {
		t.Fatalf("HasRecentAnomaly before insert failed: %v", err)
	}
	if recent {
		t.Fatalf("expected no recent anomaly before recording")
	}

	// Record anomaly
	if err := s.RecordAnomaly(proj.ID, anom); err != nil {
		t.Fatalf("RecordAnomaly failed: %v", err)
	}

	// Now should report recent anomaly
	recent, err = s.HasRecentAnomaly(proj.ID, anom.Type, 30*time.Second)
	if err != nil {
		t.Fatalf("HasRecentAnomaly after insert failed: %v", err)
	}
	if !recent {
		t.Fatalf("expected recent anomaly to be detected")
	}

	// Different anomaly type should report false
	recentOther, err := s.HasRecentAnomaly(proj.ID, "high_latency", 30*time.Second)
	if err != nil {
		t.Fatalf("HasRecentAnomaly for other type failed: %v", err)
	}
	if recentOther {
		t.Fatalf("expected false for different anomaly type")
	}
}
