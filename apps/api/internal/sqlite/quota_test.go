package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestExistingDatabaseAboveLimitFailsBeforeMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "oversized.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE oversized(data BLOB); INSERT INTO oversized VALUES(zeroblob(2097152))`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = OpenWithLimit(path, 1<<20)
	if err == nil {
		_ = db.Close()
		t.Fatal("accepted existing database above configured limit")
	}
}

func TestPageQuotaAppliesToEveryConnectionAndRejectsGrowth(t *testing.T) {
	path := filepath.Join(t.TempDir(), "quota.db")
	db, err := OpenWithLimit(path, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var size int64
	if err := db.QueryRow(`PRAGMA page_size`).Scan(&size); err != nil {
		t.Fatal(err)
	}
	conns := make([]*sql.Conn, 0, 3)
	for i := 0; i < 3; i++ {
		c, err := db.Conn(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		conns = append(conns, c)
		var pages int64
		if err := c.QueryRowContext(context.Background(), `PRAGMA max_page_count`).Scan(&pages); err != nil || pages != (1<<20)/size {
			t.Fatalf("connection %d page cap %d size %d: %v", i, pages, size, err)
		}
	}
	for _, c := range conns {
		_ = c.Close()
	}
	if _, err := db.Exec(`CREATE TABLE large_test(data BLOB)`); err != nil {
		t.Fatal(err)
	}
	full := false
	for i := 0; i < 100; i++ {
		if _, err = db.Exec(`INSERT INTO large_test VALUES(zeroblob(65536))`); err != nil {
			full = true
			break
		}
	}
	if !full {
		t.Fatal("main file was allowed to exceed configured quota")
	}
	var count int64
	if err := db.QueryRow(`PRAGMA page_count`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count*size > 1<<20 {
		t.Fatalf("pages exceed quota: %d", count*size)
	}
	reopened, err := OpenWithLimit(path, 1<<20)
	if err != nil {
		t.Fatalf("reopening at limit: %v", err)
	}
	_ = reopened.Close()
}
