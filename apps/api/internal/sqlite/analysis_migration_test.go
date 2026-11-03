package sqlite_test

import (
	"path/filepath"
	"testing"

	"traffic-center/internal/sqlite"
)

func TestAnalysisJobsSchemaSurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.db")
	db, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO anomalies (project_id,type,severity,message,value,threshold,analysis,detected_at) VALUES ('p','x','high','x',1,1,'',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO analysis_jobs (anomaly_id, project_id, state_json, status, created_at) VALUES (1, 'p', '{}', 'pending', 1)`); err != nil {
		t.Fatalf("insert analysis job: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var status string
	if err := db.QueryRow(`SELECT status FROM analysis_jobs WHERE anomaly_id = 1`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "pending" {
		t.Fatalf("status = %q, want pending", status)
	}
}
