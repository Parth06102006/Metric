package sqlite_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	anomalysqlite "traffic-center/anomaly/repository/sqlite"
	"traffic-center/domain"
	storesqlite "traffic-center/internal/sqlite"
)

func TestConcurrentQueueCapacityAndClaims(t *testing.T) {
	for _, mode := range []string{"file", "memory"} {
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "concurrent.db")
			if mode == "memory" {
				path = ":memory:"
			}
			testConcurrentQueueCapacityAndClaims(t, path)
		})
	}
}

func testConcurrentQueueCapacityAndClaims(t *testing.T, path string) {
	t.Helper()
	db, err := storesqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := anomalysqlite.New(db)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	const writers, capacity = 20, 5
	start := make(chan struct{})
	results := make(chan error, writers)
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			a := domain.Anomaly{Type: "test", Severity: "high", Message: fmt.Sprint(i), Analysis: "rule", DetectedAt: time.Now()}
			results <- repo.SaveWithAnalysis(ctx, "p", &a, domain.Snapshot{}, capacity)
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Errorf("save: %v", err)
		}
	}
	var jobs, incidents, skipped int
	if err := db.QueryRow(`SELECT COUNT(*) FROM analysis_jobs`).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM anomalies`).Scan(&incidents); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM anomalies WHERE analysis_status='skipped'`).Scan(&skipped); err != nil {
		t.Fatal(err)
	}
	if jobs != capacity || incidents != writers || skipped != writers-capacity {
		t.Fatalf("jobs=%d incidents=%d skipped=%d", jobs, incidents, skipped)
	}

	claims := make(chan anomalysqlite.AnalysisJob, writers)
	claimErrors := make(chan error, writers)
	start = make(chan struct{})
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			job, err := repo.ClaimAnalysis(ctx, time.Now(), time.Minute)
			if errors.Is(err, sql.ErrNoRows) {
				return
			}
			if err != nil {
				claimErrors <- err
				return
			}
			claims <- job
		}()
	}
	close(start)
	wg.Wait()
	close(claims)
	close(claimErrors)
	for err := range claimErrors {
		t.Errorf("claim: %v", err)
	}
	ids := map[int64]bool{}
	for job := range claims {
		if ids[job.AnomalyID] {
			t.Errorf("duplicate claim for incident %d", job.AnomalyID)
		}
		ids[job.AnomalyID] = true
	}
	if len(ids) != capacity {
		t.Fatalf("claimed %d unique jobs, want %d", len(ids), capacity)
	}
}
