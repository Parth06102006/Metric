package sqlite_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"traffic-center/domain"
	dbstore "traffic-center/internal/sqlite"
	"traffic-center/metrics/repository/sqlite"
)

func TestHistoryBudgetKeepsNewestAndRangeFailsClosed(t *testing.T) {
	db, err := dbstore.Open(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO projects(id,name,api_key_hash) VALUES('p','p','hash')`); err != nil {
		t.Fatal(err)
	}
	r := sqlite.New(db)
	ctx := context.Background()
	base := time.Now().UTC()
	for i := 0; i < 3; i++ {
		s := domain.Snapshot{Ts: base.Add(time.Duration(i) * time.Second), RPS: float64(i), Endpoints: map[string]int{strings.Repeat("x", 2<<20): 1}}
		if err := r.Save(ctx, "p", s); err != nil {
			t.Fatal(err)
		}
	}
	got, err := r.Recent(ctx, "p", 3)
	if err != nil || len(got) != 1 || got[0].RPS != 2 {
		t.Fatalf("expected newest bounded suffix: len=%d err=%v", len(got), err)
	}
	if _, err := r.Range(ctx, "p", base.Add(-time.Second), base.Add(10*time.Second), 3); !errors.Is(err, domain.ErrCapacity) {
		t.Fatalf("range should fail closed: %v", err)
	}
	s := domain.Snapshot{Ts: base.Add(4 * time.Second), Endpoints: map[string]int{strings.Repeat("x", 5<<20): 1}}
	if err := r.Save(ctx, "p", s); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Recent(ctx, "p", 3); !errors.Is(err, domain.ErrCapacity) {
		t.Fatalf("oversized newest row accepted: %v", err)
	}
}
