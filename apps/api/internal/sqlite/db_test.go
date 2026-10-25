package sqlite_test

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"

	"traffic-center/internal/sqlite"
)

const legacySchema = `
CREATE TABLE projects (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	api_key_hash TEXT NOT NULL,
	created_at TEXT NOT NULL
);
CREATE TABLE thresholds (
	project_id TEXT PRIMARY KEY,
	p95_ms REAL NOT NULL DEFAULT 1000,
	error_rate REAL NOT NULL DEFAULT 5,
	rps_max REAL NOT NULL DEFAULT 100,
	req_per_ip_max REAL NOT NULL DEFAULT 30
);
CREATE TABLE metric_snapshots (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	project_id TEXT NOT NULL,
	created_at TEXT NOT NULL,
	data_json TEXT NOT NULL
);
CREATE TABLE anomalies (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	project_id TEXT NOT NULL,
	created_at TEXT NOT NULL,
	severity TEXT NOT NULL,
	summary TEXT NOT NULL,
	analysis TEXT NOT NULL
);
CREATE TABLE alerts (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	project_id TEXT NOT NULL,
	created_at TEXT NOT NULL,
	message TEXT NOT NULL,
	status TEXT NOT NULL DEFAULT 'open'
);
`

func TestMigrateLegacySchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")

	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	if _, err := raw.Exec(legacySchema); err != nil {
		t.Fatalf("create legacy schema: %v", err)
	}
	if _, err := raw.Exec(
		`INSERT INTO projects (id, name, api_key_hash, created_at) VALUES ('p1', 'Legacy', 'hash', '2026-01-01T00:00:00Z')`,
	); err != nil {
		t.Fatalf("insert legacy project: %v", err)
	}
	if _, err := raw.Exec(
		`INSERT INTO metric_snapshots (project_id, created_at, data_json) VALUES (?, ?, ?)`,
		"p1", "2026-01-01T00:00:00Z",
		`{"ts":"2026-01-01T00:00:00Z","window_s":60,"rps":1.5,"p50":10,"p95":20,"p99":30,"error_rate":1,"unique_ips":2,"top_ip_rps":0.5,"avg_payload_bytes":100,"endpoints":{"/a":2}}`,
	); err != nil {
		t.Fatalf("insert legacy snapshot: %v", err)
	}
	if _, err := raw.Exec(
		`INSERT INTO anomalies (project_id, created_at, severity, summary, analysis) VALUES (?, ?, ?, ?, ?)`,
		"p1", "2026-01-01T00:00:00Z", "high", "high_latency: P95 too high", "because",
	); err != nil {
		t.Fatalf("insert legacy anomaly: %v", err)
	}
	raw.Close()

	db, err := sqlite.Open(path)
	if err != nil {
		t.Fatalf("open migrated: %v", err)
	}
	defer db.Close()

	var snapshotCount int
	if err := db.QueryRow(`SELECT COUNT(1) FROM metric_snapshots`).Scan(&snapshotCount); err != nil {
		t.Fatalf("count snapshots: %v", err)
	}
	if snapshotCount != 1 {
		t.Fatalf("snapshots = %d, want 1", snapshotCount)
	}

	var rps float64
	if err := db.QueryRow(`SELECT rps FROM metric_snapshots LIMIT 1`).Scan(&rps); err != nil {
		t.Fatalf("read rps: %v", err)
	}
	if rps != 1.5 {
		t.Fatalf("rps = %f, want 1.5", rps)
	}

	var anomalyType, message string
	if err := db.QueryRow(`SELECT type, message FROM anomalies LIMIT 1`).Scan(&anomalyType, &message); err != nil {
		t.Fatalf("read anomaly: %v", err)
	}
	if anomalyType != "high_latency" || message != "P95 too high" {
		t.Fatalf("anomaly = %q/%q", anomalyType, message)
	}

	var alerts int
	if err := db.QueryRow(`SELECT COUNT(1) FROM sqlite_master WHERE type = 'table' AND name = 'alerts'`).Scan(&alerts); err != nil {
		t.Fatalf("check alerts table: %v", err)
	}
	if alerts != 0 {
		t.Fatal("alerts table should be dropped")
	}

	var ownerEmail string
	if err := db.QueryRow(`SELECT owner_email FROM projects WHERE id = 'p1'`).Scan(&ownerEmail); err != nil {
		t.Fatalf("read migrated owner_email: %v", err)
	}
	if ownerEmail != "" {
		t.Fatalf("legacy project owner_email = %q, want empty string", ownerEmail)
	}

	var revoked int
	if err := db.QueryRow(`SELECT api_key_revoked FROM projects WHERE id = 'p1'`).Scan(&revoked); err != nil {
		t.Fatalf("read migrated api_key_revoked: %v", err)
	}
	if revoked != 0 {
		t.Fatalf("legacy project api_key_revoked = %d, want 0", revoked)
	}
}
