package sqlite_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"traffic-center/domain"
	dbstore "traffic-center/internal/sqlite"
	"traffic-center/metrics/repository/sqlite"
)

func TestRecentHistoryPersistsAndLimitsNewestWithoutChangingRange(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "history.db")
	db, err := dbstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	repo := sqlite.New(db)
	base := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	// Deliberately insert out of order; same-timestamp ties use insertion order.
	for i, n := range []int{3, 1, 2, 4, 4} {
		rps := float64(n)
		if i == 4 {
			rps = 44
		}
		if err := repo.Save(ctx, "a", domain.Snapshot{Ts: base.Add(time.Duration(n) * time.Second), RPS: rps, Endpoints: map[string]int{}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := repo.Save(ctx, "b", domain.Snapshot{Ts: base.Add(10 * time.Second), RPS: 99}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = dbstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo = sqlite.New(db)
	recent, err := repo.Recent(ctx, "a", 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(recent) != 3 || recent[0].RPS != 3 || recent[1].RPS != 4 || recent[2].RPS != 44 {
		t.Fatalf("recent: %+v", recent)
	}
	last, err := repo.Recent(ctx, "a", 1)
	if err != nil || len(last) != 1 || last[0].RPS != 44 {
		t.Fatalf("tie limit: %+v, %v", last, err)
	}
	rangeRows, err := repo.Range(ctx, "a", base, base.Add(10*time.Second), 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(rangeRows) != 2 || rangeRows[0].RPS != 1 || rangeRows[1].RPS != 2 {
		t.Fatalf("range changed: %+v", rangeRows)
	}
	empty, err := repo.Recent(ctx, "missing", 3)
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("empty = %+v, %v", empty, err)
	}
}
