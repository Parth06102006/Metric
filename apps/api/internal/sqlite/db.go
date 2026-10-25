package sqlite

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// RepositoryProxyHelper exposes the underlying *sql.DB for integration tests
// that need to insert raw rows (e.g. legacy projects with no owner).
type RepositoryProxyHelper struct {
	DB *sql.DB
}

const schemaVersion = 5

// Open opens the SQLite database, applies pragmas, and runs migrations.
func Open(path string) (*sql.DB, error) {
	var dsn string
	switch {
	case path == ":memory:":
		dsn = "file::memory:?cache=shared&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)"
	case strings.Contains(path, "?"):
		dsn = path
	default:
		dsn = path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)&_pragma=synchronous(NORMAL)"
	}

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)

	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	if err := Migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

const baseSchema = `
CREATE TABLE IF NOT EXISTS projects (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	api_key_hash TEXT NOT NULL,
	created_at INTEGER NOT NULL DEFAULT 0,
	owner_email TEXT NOT NULL DEFAULT '',
	api_key_revoked INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS thresholds (
	project_id TEXT PRIMARY KEY,
	p95_ms REAL NOT NULL DEFAULT 1000,
	error_rate REAL NOT NULL DEFAULT 5,
	rps_max REAL NOT NULL DEFAULT 100,
	req_per_ip_max REAL NOT NULL DEFAULT 30,
	FOREIGN KEY(project_id) REFERENCES projects(id) ON DELETE CASCADE
);
`

const snapshotTable = `
CREATE TABLE IF NOT EXISTS metric_snapshots (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	project_id TEXT NOT NULL,
	ts INTEGER NOT NULL,
	window_s INTEGER NOT NULL,
	rps REAL NOT NULL,
	p50 REAL NOT NULL,
	p95 REAL NOT NULL,
	p99 REAL NOT NULL,
	error_rate REAL NOT NULL,
	unique_ips INTEGER NOT NULL,
	top_ip_rps REAL NOT NULL,
	avg_payload_bytes REAL NOT NULL,
	endpoints_json TEXT NOT NULL DEFAULT '{}'
);
`

const snapshotIndexes = `
CREATE INDEX IF NOT EXISTS idx_snapshots_project_ts
	ON metric_snapshots(project_id, ts DESC);
`

const anomalyTable = `
CREATE TABLE IF NOT EXISTS anomalies (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	project_id TEXT NOT NULL,
	type TEXT NOT NULL,
	severity TEXT NOT NULL,
	message TEXT NOT NULL,
	value REAL NOT NULL,
	threshold REAL NOT NULL,
	analysis TEXT NOT NULL DEFAULT '',
	detected_at INTEGER NOT NULL
);
`

const anomalyIndexes = `
CREATE INDEX IF NOT EXISTS idx_anomalies_project_detected
	ON anomalies(project_id, detected_at DESC);
CREATE INDEX IF NOT EXISTS idx_anomalies_project_type_detected
	ON anomalies(project_id, type, detected_at DESC);
`

const auditTable = `
CREATE TABLE IF NOT EXISTS audit_events (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	ts INTEGER NOT NULL,
	actor_email TEXT NOT NULL DEFAULT '',
	action TEXT NOT NULL,
	target_project_id TEXT NOT NULL DEFAULT '',
	outcome TEXT NOT NULL,
	request_id TEXT NOT NULL DEFAULT '',
	details_json TEXT NOT NULL DEFAULT '{}'
);
`

const auditIndexes = `
CREATE INDEX IF NOT EXISTS idx_audit_ts
	ON audit_events(ts DESC);
CREATE INDEX IF NOT EXISTS idx_audit_action_ts
	ON audit_events(action, ts DESC);
`

// Migrate creates the schema and upgrades the v1 layout (JSON snapshot blobs,
// text timestamps, dead alerts table) in place.
func Migrate(db *sql.DB) error {
	if _, err := db.Exec(baseSchema); err != nil {
		return fmt.Errorf("base schema: %w", err)
	}

	if !columnExists(db, "projects", "owner_email") {
		if _, err := db.Exec(`ALTER TABLE projects ADD COLUMN owner_email TEXT NOT NULL DEFAULT ''`); err != nil {
			return fmt.Errorf("add owner_email to projects: %w", err)
		}
	}

	if !columnExists(db, "projects", "api_key_revoked") {
		if _, err := db.Exec(`ALTER TABLE projects ADD COLUMN api_key_revoked INTEGER NOT NULL DEFAULT 0`); err != nil {
			return fmt.Errorf("add api_key_revoked to projects: %w", err)
		}
	}

	if columnExists(db, "metric_snapshots", "data_json") {
		if err := migrateSnapshots(db); err != nil {
			return fmt.Errorf("migrate snapshots: %w", err)
		}
	}

	if columnExists(db, "anomalies", "summary") && !columnExists(db, "anomalies", "type") {
		if err := migrateAnomalies(db); err != nil {
			return fmt.Errorf("migrate anomalies: %w", err)
		}
	}

	if _, err := db.Exec(snapshotTable + snapshotIndexes + anomalyTable + anomalyIndexes + auditTable + auditIndexes); err != nil {
		return fmt.Errorf("schema: %w", err)
	}

	// The alerts table was never written to; drop it rather than carry dead schema.
	_, _ = db.Exec(`DROP TABLE IF EXISTS alerts`)
	_, _ = db.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, schemaVersion))
	return nil
}

type legacySnapshot struct {
	Ts              time.Time      `json:"ts"`
	WindowS         int            `json:"window_s"`
	RPS             float64        `json:"rps"`
	P50             float64        `json:"p50"`
	P95             float64        `json:"p95"`
	P99             float64        `json:"p99"`
	ErrorRate       float64        `json:"error_rate"`
	UniqueIPs       int            `json:"unique_ips"`
	TopIPRPS        float64        `json:"top_ip_rps"`
	AvgPayloadBytes float64        `json:"avg_payload_bytes"`
	Endpoints       map[string]int `json:"endpoints"`
}

func migrateSnapshots(db *sql.DB) error {
	type row struct {
		projectID string
		createdAt string
		data      string
	}

	rows, err := db.Query(`SELECT project_id, created_at, data_json FROM metric_snapshots`)
	if err != nil {
		return err
	}

	var legacy []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.projectID, &r.createdAt, &r.data); err != nil {
			rows.Close()
			return err
		}
		legacy = append(legacy, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`ALTER TABLE metric_snapshots RENAME TO metric_snapshots_legacy_v1`); err != nil {
		return err
	}
	if _, err := tx.Exec(snapshotTable + snapshotIndexes); err != nil {
		return err
	}

	for _, r := range legacy {
		var snapshot legacySnapshot
		if err := json.Unmarshal([]byte(r.data), &snapshot); err != nil {
			continue
		}
		ts := snapshot.Ts
		if ts.IsZero() {
			ts, _ = time.Parse(time.RFC3339Nano, r.createdAt)
		}
		endpoints, _ := json.Marshal(snapshot.Endpoints)
		if _, err := tx.Exec(
			`INSERT INTO metric_snapshots
				(project_id, ts, window_s, rps, p50, p95, p99, error_rate, unique_ips, top_ip_rps, avg_payload_bytes, endpoints_json)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			r.projectID, ts.UnixMilli(), snapshot.WindowS, snapshot.RPS, snapshot.P50, snapshot.P95,
			snapshot.P99, snapshot.ErrorRate, snapshot.UniqueIPs, snapshot.TopIPRPS,
			snapshot.AvgPayloadBytes, string(endpoints),
		); err != nil {
			return err
		}
	}

	return tx.Commit()
}

func migrateAnomalies(db *sql.DB) error {
	type row struct {
		projectID string
		createdAt string
		severity  string
		summary   string
		analysis  string
	}

	rows, err := db.Query(`SELECT project_id, created_at, severity, summary, analysis FROM anomalies`)
	if err != nil {
		return err
	}

	var legacy []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.projectID, &r.createdAt, &r.severity, &r.summary, &r.analysis); err != nil {
			rows.Close()
			return err
		}
		legacy = append(legacy, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`ALTER TABLE anomalies RENAME TO anomalies_legacy_v1`); err != nil {
		return err
	}
	if _, err := tx.Exec(anomalyTable + anomalyIndexes); err != nil {
		return err
	}

	for _, r := range legacy {
		detectedAt, _ := time.Parse(time.RFC3339Nano, r.createdAt)
		if detectedAt.IsZero() {
			detectedAt = time.Now().UTC()
		}

		anomalyType, message := r.summary, r.summary
		if parts := strings.SplitN(r.summary, ":", 2); len(parts) == 2 {
			anomalyType = strings.TrimSpace(parts[0])
			message = strings.TrimSpace(parts[1])
		}

		if _, err := tx.Exec(
			`INSERT INTO anomalies
				(project_id, type, severity, message, value, threshold, analysis, detected_at)
			 VALUES (?, ?, ?, ?, 0, 0, ?, ?)`,
			r.projectID, anomalyType, r.severity, message, r.analysis, detectedAt.UnixMilli(),
		); err != nil {
			return err
		}
	}

	return tx.Commit()
}

func columnExists(db *sql.DB, table, column string) bool {
	rows, err := db.Query(fmt.Sprintf(`PRAGMA table_info(%q)`, table))
	if err != nil {
		return false
	}
	defer rows.Close()

	for rows.Next() {
		var (
			cid       int
			name      string
			columnTyp string
			notNull   int
			defaultV  any
			pk        int
		)
		if err := rows.Scan(&cid, &name, &columnTyp, &notNull, &defaultV, &pk); err != nil {
			return false
		}
		if name == column {
			return true
		}
	}
	return false
}
