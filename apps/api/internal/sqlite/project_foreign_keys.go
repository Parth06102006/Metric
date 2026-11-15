package sqlite

import (
	"database/sql"
	"fmt"
	"strings"
)

// Rebuild on the same transaction/connection with foreign_keys left ON. Rebuild
// the child job table too: dropping the old anomalies table must not cascade
// away valid pending jobs. Never rename the old parent out from under its child.
func migrateProjectForeignKeys(db *sql.DB) error {
	for _, table := range []string{"metric_snapshots", "anomalies", "analysis_jobs"} {
		var count int
		if err := db.QueryRow(`SELECT count(*) FROM pragma_foreign_key_list(?) WHERE "table"='projects' AND on_delete='CASCADE'`, table).Scan(&count); err != nil {
			return err
		}
		if count == 0 {
			break
		}
		if table == "analysis_jobs" {
			return nil
		}
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	snapshotDDL := strings.Replace(snapshotTable, "metric_snapshots (", "metric_snapshots_v8 (", 1)
	snapshotDDL = strings.Replace(snapshotDDL, "project_id TEXT NOT NULL,", "project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,", 1)
	anomalyDDL := strings.Replace(anomalyTable, "anomalies (", "anomalies_v8 (", 1)
	anomalyDDL = strings.Replace(anomalyDDL, "project_id TEXT NOT NULL,", "project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,", 1)
	anomalyDDL = strings.Replace(anomalyDDL, "detected_at INTEGER NOT NULL", "detected_at INTEGER NOT NULL, diagnosis_json TEXT NOT NULL DEFAULT '', analysis_status TEXT NOT NULL DEFAULT 'disabled'", 1)
	if _, err = tx.Exec(snapshotDDL + anomalyDDL + `
 CREATE TABLE analysis_jobs_v8 (
 anomaly_id INTEGER PRIMARY KEY REFERENCES anomalies_v8(id) ON DELETE CASCADE,
 project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
 state_json TEXT NOT NULL,
 status TEXT NOT NULL DEFAULT 'pending',
 attempts INTEGER NOT NULL DEFAULT 0,
 next_attempt INTEGER NOT NULL DEFAULT 0,
 lease_until INTEGER NOT NULL DEFAULT 0,
 lease_token TEXT NOT NULL DEFAULT '',
 created_at INTEGER NOT NULL
 );`); err != nil {
		return err
	}
	for _, stmt := range []string{
		`INSERT INTO metric_snapshots_v8 (id,project_id,ts,window_s,rps,p50,p95,p99,error_rate,unique_ips,top_ip_rps,avg_payload_bytes,endpoints_json)
		 SELECT s.id,s.project_id,s.ts,s.window_s,s.rps,s.p50,s.p95,s.p99,s.error_rate,s.unique_ips,s.top_ip_rps,s.avg_payload_bytes,s.endpoints_json FROM metric_snapshots s JOIN projects p ON p.id=s.project_id`,
		`INSERT INTO anomalies_v8 (id,project_id,type,severity,message,value,threshold,analysis,detected_at,diagnosis_json,analysis_status) SELECT a.id,a.project_id,a.type,a.severity,a.message,a.value,a.threshold,a.analysis,a.detected_at,a.diagnosis_json,a.analysis_status FROM anomalies a JOIN projects p ON p.id=a.project_id`,
		`INSERT INTO analysis_jobs_v8 (anomaly_id,project_id,state_json,status,attempts,next_attempt,lease_until,lease_token,created_at)
		 SELECT j.anomaly_id,j.project_id,j.state_json,j.status,j.attempts,j.next_attempt,j.lease_until,j.lease_token,j.created_at FROM analysis_jobs j JOIN anomalies_v8 a ON a.id=j.anomaly_id AND a.project_id=j.project_id`,
		// Preserve high-water marks, including IDs of previously deleted rows. A
		// worker holding an obsolete lease must never see a reused incident ID.
		`UPDATE sqlite_sequence SET seq=MAX(seq,COALESCE((SELECT seq FROM sqlite_sequence WHERE name='metric_snapshots'),0)) WHERE name='metric_snapshots_v8'`,
		`UPDATE sqlite_sequence SET seq=MAX(seq,COALESCE((SELECT seq FROM sqlite_sequence WHERE name='anomalies'),0)) WHERE name='anomalies_v8'`,
		`DROP TABLE analysis_jobs`,
		`DROP TABLE anomalies`,
		`DROP TABLE metric_snapshots`,
		`ALTER TABLE anomalies_v8 RENAME TO anomalies`,
		`ALTER TABLE metric_snapshots_v8 RENAME TO metric_snapshots`,
		`ALTER TABLE analysis_jobs_v8 RENAME TO analysis_jobs`,
		snapshotIndexes + anomalyIndexes + `CREATE INDEX idx_analysis_jobs_ready ON analysis_jobs(status,next_attempt,lease_until); CREATE INDEX idx_analysis_jobs_project ON analysis_jobs(project_id);`,
	} {
		if _, err = tx.Exec(stmt); err != nil {
			return fmt.Errorf("rebuild project data: %w", err)
		}
	}
	rows, err := tx.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		return err
	}
	invalid := rows.Next()
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if invalid {
		return fmt.Errorf("foreign key violations after migration")
	}
	return tx.Commit()
}
