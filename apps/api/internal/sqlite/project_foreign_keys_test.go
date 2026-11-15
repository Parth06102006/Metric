package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	anomalies "traffic-center/anomaly/repository/sqlite"
	"traffic-center/domain"
	snapshots "traffic-center/metrics/repository/sqlite"
	projects "traffic-center/project/repository/sqlite"
)

func TestProjectForeignKeysMigrationPreservesValidRowsAndLeases(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v7.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	// Recreate the actual pre-v8 layout; FK enforcement enabled, parent rows
	// absent for orphan telemetry as allowed by that schema.
	_, err = raw.Exec(`PRAGMA foreign_keys=ON;` + baseSchema + snapshotTable + snapshotIndexes + anomalyTable + anomalyIndexes + `
 ALTER TABLE anomalies ADD COLUMN diagnosis_json TEXT NOT NULL DEFAULT '';
 ALTER TABLE anomalies ADD COLUMN analysis_status TEXT NOT NULL DEFAULT 'disabled';` + analysisJobsSchema + `
 INSERT INTO projects(id,name,api_key_hash) VALUES ('p','kept','hash');
 INSERT INTO metric_snapshots(id,project_id,ts,window_s,rps,p50,p95,p99,error_rate,unique_ips,top_ip_rps,avg_payload_bytes,endpoints_json) VALUES
 (7,'p',123,60,2,3,4,5,6,7,8,9,'{"/kept":2}'),(8,'gone',123,60,2,3,4,5,6,7,8,9,'{}');
 INSERT INTO anomalies(id,project_id,type,severity,message,value,threshold,analysis,detected_at,diagnosis_json,analysis_status) VALUES
 (10,'p','x','high','kept',1,2,'analysis',123,'{"pattern":"unclear"}','pending'),
 (11,'gone','x','high','orphan',1,2,'analysis',123,'','pending');
 INSERT INTO analysis_jobs(anomaly_id,project_id,state_json,status,attempts,next_attempt,lease_until,lease_token,created_at) VALUES
 (10,'p','{"state":true}','processing',2,42,99,'lease',123), (11,'gone','{}','pending',0,0,0,'',123);
 UPDATE sqlite_sequence SET seq=100 WHERE name='anomalies';
 PRAGMA user_version=7;
 `)
	if err != nil {
		t.Fatal(err)
	}
	// The v8 rebuild preserves the lease; v9 fails it closed because old
	// processing state cannot reveal whether the provider was contacted.
	if err := migrateProjectForeignKeys(raw); err != nil {
		t.Fatal(err)
	}
	var id, attempts, next, until int
	var state, token, diagnosis, endpoints string
	if err := raw.QueryRow(`SELECT anomaly_id,attempts,next_attempt,lease_until,state_json,lease_token FROM analysis_jobs`).Scan(&id, &attempts, &next, &until, &state, &token); err != nil {
		t.Fatal(err)
	}
	if id != 10 || attempts != 2 || next != 42 || until != 99 || state != `{"state":true}` || token != "lease" {
		t.Fatal("job changed during rebuild")
	}
	if err = raw.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, table := range []string{"metric_snapshots", "anomalies"} {
		var count int
		if err := db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&count); err != nil || count != 1 {
			t.Fatalf("%s count=%d err=%v", table, count, err)
		}
	}
	var remaining int
	if err := db.QueryRow(`SELECT count(*) FROM analysis_jobs`).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("old processing job must not be retried: %d %v", remaining, err)
	}
	var status string
	if err := db.QueryRow(`SELECT analysis_status FROM anomalies`).Scan(&status); err != nil || status != "failed" {
		t.Fatalf("expected uncertain analysis to fail: %s %v", status, err)
	}
	if err := db.QueryRow(`SELECT diagnosis_json FROM anomalies`).Scan(&diagnosis); err != nil || diagnosis != `{"pattern":"unclear"}` {
		t.Fatalf("diagnosis=%s %v", diagnosis, err)
	}
	if err := db.QueryRow(`SELECT endpoints_json FROM metric_snapshots`).Scan(&endpoints); err != nil || endpoints != `{"/kept":2}` {
		t.Fatalf("endpoints=%s %v", endpoints, err)
	}
	if err := Migrate(db); err != nil {
		t.Fatalf("repeat migration: %v", err)
	}
	a := domain.Anomaly{DetectedAt: time.Now()}
	if err := anomalies.New(db).Save(context.Background(), "p", &a); err != nil || a.ID <= 100 {
		t.Fatalf("high-water ID=%d err=%v", a.ID, err)
	}
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 9 {
		t.Fatalf("version=%d %v", version, err)
	}
}

func TestProjectForeignKeysMigrationRollsBackOnFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollback.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	// A pre-existing unrelated FK violation must fail the check, rolling back
	// all telemetry table swaps instead of leaving a partially migrated database.
	if _, err := raw.Exec(baseSchema + snapshotTable + anomalyTable + `
	ALTER TABLE anomalies ADD COLUMN diagnosis_json TEXT NOT NULL DEFAULT '';
	ALTER TABLE anomalies ADD COLUMN analysis_status TEXT NOT NULL DEFAULT 'disabled';` + analysisJobsSchema + `
	INSERT INTO projects(id,name,api_key_hash) VALUES ('p','test','hash');
	INSERT INTO anomalies(id,project_id,type,severity,message,value,threshold,detected_at) VALUES(12,'p','x','high','kept',1,1,1);
	INSERT INTO thresholds(project_id) VALUES('missing');
	PRAGMA user_version=7;`); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`PRAGMA foreign_keys=ON`); err != nil {
		t.Fatal(err)
	}
	if err := migrateProjectForeignKeys(raw); err == nil {
		t.Fatal("migration accepted corrupt foreign keys")
	}
	var count, version int
	if err := raw.QueryRow(`SELECT count(*) FROM anomalies WHERE id=12`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("original data changed: count=%d %v", count, err)
	}
	if err := raw.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name IN ('metric_snapshots_v8','anomalies_v8','analysis_jobs_v8')`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("partial tables remain: %d %v", count, err)
	}
	if err := raw.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 7 {
		t.Fatalf("version advanced on failure: %d %v", version, err)
	}
}

func TestMigrationPreservesSequenceWhenNoRowsSurvive(t *testing.T) {
	raw, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "empty.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err := raw.Exec(baseSchema + snapshotTable + anomalyTable + `
	ALTER TABLE anomalies ADD COLUMN diagnosis_json TEXT NOT NULL DEFAULT '';
	ALTER TABLE anomalies ADD COLUMN analysis_status TEXT NOT NULL DEFAULT 'disabled';` + analysisJobsSchema + `
	INSERT INTO anomalies(id,project_id,type,severity,message,value,threshold,detected_at) VALUES(100,'gone','x','high','orphan',1,1,1);
	PRAGMA foreign_keys=ON;`); err != nil {
		t.Fatal(err)
	}
	if err := migrateProjectForeignKeys(raw); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`INSERT INTO projects(id,name,api_key_hash) VALUES('p','test','hash')`); err != nil {
		t.Fatal(err)
	}
	a := domain.Anomaly{DetectedAt: time.Now()}
	if err := anomalies.New(raw).Save(context.Background(), "p", &a); err != nil || a.ID <= 100 {
		t.Fatalf("reused discarded ID: %d %v", a.ID, err)
	}
}

func TestProjectDeleteRejectsLateWritesAndCascadesJobs(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	pr := projects.New(db)
	ar := anomalies.New(db)
	sr := snapshots.New(db)
	if err := pr.Create(ctx, domain.Project{ID: "p", Name: "test"}, "hash"); err != nil {
		t.Fatal(err)
	}
	a := domain.Anomaly{DetectedAt: time.Now()}
	if err := ar.SaveWithAnalysis(ctx, "p", &a, domain.Snapshot{}, 10); err != nil {
		t.Fatal(err)
	}
	job, err := ar.ClaimAnalysis(ctx, time.Now(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := sr.Save(ctx, "p", domain.Snapshot{Ts: time.Now()}); err != nil {
		t.Fatal(err)
	}
	// Delete only the parent to verify database cascades, not repository cleanup.
	if _, err := db.Exec(`DELETE FROM projects WHERE id='p'`); err != nil {
		t.Fatal(err)
	}
	if err := sr.Save(ctx, "p", domain.Snapshot{Ts: time.Now()}); err == nil {
		t.Fatal("late snapshot accepted")
	}
	if err := ar.Save(ctx, "p", &a); err == nil {
		t.Fatal("late anomaly accepted")
	}
	if err := ar.SaveWithAnalysis(ctx, "p", &a, domain.Snapshot{}, 10); err == nil {
		t.Fatal("late queued anomaly accepted")
	}
	if err := ar.CompleteAnalysis(ctx, job, domain.Diagnosis{}, "late"); err == nil {
		t.Fatal("late analysis committed")
	}
	for _, table := range []string{"metric_snapshots", "anomalies", "analysis_jobs"} {
		var count int
		if err := db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s count=%d err=%v", table, count, err)
		}
	}
}
