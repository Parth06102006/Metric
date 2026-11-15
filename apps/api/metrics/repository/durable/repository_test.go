package durable

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"traffic-center/domain"
	dbsqlite "traffic-center/internal/sqlite"
	"traffic-center/metrics/repository/memory"
)

func record(at time.Time) domain.LogRecord {
	return domain.LogRecord{Ts: at, Endpoint: "/items", Method: "GET", Status: 200, LatencyMs: 21, IP: "192.0.2.1", PayloadBytes: 10}
}
func setup(t *testing.T) (string, *Repository) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "db.sqlite")
	db, err := dbsqlite.OpenWithLimit(path, 16<<20)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`INSERT INTO projects(id,name,api_key_hash) VALUES('p','project','hash')`); err != nil {
		t.Fatal(err)
	}
	r, err := New(db, memory.New(time.Minute), time.Minute, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	return path, r
}
func TestAppendRecoverAndDelete(t *testing.T) {
	path, r := setup(t)
	ctx := context.Background()
	input := []domain.LogRecord{record(time.Now().UTC().Add(-time.Second)), record(time.Now().UTC())}
	if err := r.Append(ctx, "p", input); err != nil {
		t.Fatal(err)
	}
	db, err := dbsqlite.OpenWithLimit(path, 16<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	restored, err := New(db, memory.New(time.Minute), time.Minute, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if err := restored.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := restored.Recent(ctx, "p", time.Time{})
	if err != nil || len(got) != 2 || !got[0].Ts.Equal(input[0].Ts) || got[1].IP != input[1].IP {
		t.Fatalf("recovery: %+v %v", got, err)
	}
	if _, err := db.Exec(`DELETE FROM projects WHERE id='p'`); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM live_batches`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("cascade: %d %v", n, err)
	}
}
func TestFailedAdmissionNeverPublishesOrPersists(t *testing.T) {
	_, r := setup(t)
	ctx := context.Background()
	r.available = func(string) (uint64, error) { return 1, nil }
	if err := r.Ready(ctx); !errors.Is(err, domain.ErrStorageUnavailable) {
		t.Fatalf("low disk should fail readiness: %v", err)
	}
	if err := r.Append(ctx, "p", []domain.LogRecord{record(time.Now())}); !errors.Is(err, domain.ErrStorageUnavailable) {
		t.Fatalf("expected low disk: %v", err)
	}
	got, _ := r.Recent(ctx, "p", time.Time{})
	if len(got) != 0 {
		t.Fatal("failed append visible")
	}
	var n int
	if err := r.db.QueryRow(`SELECT count(*) FROM live_batches`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("failed append persisted %d %v", n, err)
	}
	r.available = func(string) (uint64, error) { return 1 << 30, nil }
	if err := r.Ready(ctx); err != nil {
		t.Fatalf("healthy storage not ready: %v", err)
	}
	if err := r.Append(ctx, "missing", []domain.LogRecord{record(time.Now())}); !errors.Is(err, domain.ErrStorageUnavailable) {
		t.Fatalf("foreign key failure: %v", err)
	}
	got, _ = r.Recent(ctx, "missing", time.Time{})
	if len(got) != 0 {
		t.Fatal("failed transaction visible")
	}
	if err := r.Append(ctx, "p", []domain.LogRecord{record(time.Now())}); err != nil {
		t.Fatalf("budget not released: %v", err)
	}
}
func TestPruneAndRecoverLimits(t *testing.T) {
	_, r := setup(t)
	ctx := context.Background()
	old := []domain.LogRecord{record(time.Now().Add(-2 * time.Minute))}
	payload, _ := json.Marshal(old)
	if _, err := r.db.Exec(`INSERT INTO live_batches(project_id,latest_ns,records_json) VALUES(?,?,?)`, "p", old[0].Ts.UnixNano(), payload); err != nil {
		t.Fatal(err)
	}
	if err := r.Append(ctx, "p", []domain.LogRecord{record(time.Now()), record(time.Now())}); err != nil {
		t.Fatal(err)
	}
	n, err := r.Prune(ctx)
	if err != nil || n != 1 {
		t.Fatalf("prune %d %v", n, err)
	}
	limits := memory.DefaultLimits()
	limits.ProjectRecords = 1
	limited, err := New(r.db, memory.NewWithLimits(time.Minute, limits), time.Minute, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if err := limited.Recover(ctx); !errors.Is(err, domain.ErrCapacity) {
		t.Fatalf("recovery must fail closed: %v", err)
	}
}
func TestPrunerDrainsBacklogInBoundedTransactions(t *testing.T) {
	_, r := setup(t)
	ctx := context.Background()
	old := record(time.Now().Add(-2 * time.Minute))
	payload, _ := json.Marshal([]domain.LogRecord{old})
	for i := 0; i < 40; i++ {
		if _, err := r.db.Exec(`INSERT INTO live_batches(project_id,latest_ns,records_json) VALUES(?,?,?)`, "p", old.Ts.UnixNano(), payload); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.pruneExpired(ctx); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := r.db.QueryRow(`SELECT count(*) FROM live_batches`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("backlog not drained: %d %v", n, err)
	}
}

func TestCrashRecovery(t *testing.T) {
	if p := os.Getenv("METRIC_CRASH_TEST_DB"); p != "" {
		db, err := dbsqlite.OpenWithLimit(p, 16<<20)
		if err != nil {
			panic(err)
		}
		r, err := New(db, memory.New(time.Minute), time.Minute, 1<<20)
		if err != nil {
			panic(err)
		}
		if err := r.Append(context.Background(), "p", []domain.LogRecord{record(time.Now().UTC())}); err != nil {
			panic(err)
		}
		fmt.Println("committed")
		select {}
	}
	path, r := setup(t)
	if err := r.db.Close(); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestCrashRecovery$")
	cmd.Env = append(os.Environ(), "METRIC_CRASH_TEST_DB="+path)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil || line != "committed\n" {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatalf("child: %q %v", line, err)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	db, err := dbsqlite.OpenWithLimit(path, 16<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	recovered, err := New(db, memory.New(time.Minute), time.Minute, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if err := recovered.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err := recovered.Recent(context.Background(), "p", time.Time{})
	if err != nil || len(got) != 1 {
		t.Fatalf("crash recovery: %+v %v", got, err)
	}
}
