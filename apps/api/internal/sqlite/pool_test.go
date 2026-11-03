package sqlite_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"traffic-center/internal/sqlite"
)

func TestPoolCapAndConnectionPragmas(t *testing.T) {
	for _, path := range []string{filepath.Join(t.TempDir(), "pool.db"), filepath.Join(t.TempDir(), "custom.db") + "?cache=private", ":memory:"} {
		t.Run(path, func(t *testing.T) {
			db, err := sqlite.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if got := db.Stats().MaxOpenConnections; got != 10 {
				t.Fatalf("max open connections = %d, want 10", got)
			}
			for i := 0; i < 10; i++ {
				conn, err := db.Conn(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				defer conn.Close()
				var foreignKeys, busyTimeout int
				if err := conn.QueryRowContext(context.Background(), `PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil || foreignKeys != 1 {
					t.Fatalf("connection %d foreign keys = %d: %v", i, foreignKeys, err)
				}
				if err := conn.QueryRowContext(context.Background(), `PRAGMA busy_timeout`).Scan(&busyTimeout); err != nil || busyTimeout != 5000 {
					t.Fatalf("connection %d busy timeout = %d: %v", i, busyTimeout, err)
				}
			}
			if got := db.Stats().InUse; got != 10 {
				t.Fatalf("in-use connections = %d, want 10", got)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()
			conn, err := db.Conn(ctx)
			if conn != nil {
				conn.Close()
			}
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("11th connection: %v, want deadline", err)
			}
			if db.Stats().WaitCount == 0 {
				t.Fatal("pool did not record waiting for a connection")
			}
		})
	}
}

func TestSeparateMemoryDatabasesAreIsolated(t *testing.T) {
	first, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if _, err := first.Exec(`INSERT INTO projects(id,name,api_key_hash) VALUES('p','one','hash')`); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := second.QueryRow(`SELECT COUNT(*) FROM projects`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("independent memory db has %d projects: %v", n, err)
	}
}
