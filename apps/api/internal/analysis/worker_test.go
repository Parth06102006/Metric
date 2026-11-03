package analysis

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	anomalysqlite "traffic-center/anomaly/repository/sqlite"
	"traffic-center/domain"
	"traffic-center/internal/jev"
	storesqlite "traffic-center/internal/sqlite"
)

type blockingAnalyst struct {
	started chan struct{}
	release chan struct{}
}
type eventSink struct{ events chan domain.Anomaly }

func (s *eventSink) BroadcastSnapshot(string, domain.Snapshot)   {}
func (s *eventSink) BroadcastAnomaly(_ string, a domain.Anomaly) { s.events <- a }
func (s *eventSink) Drop(string)                                 {}
func (a *blockingAnalyst) Diagnose(ctx context.Context, state jev.State) (jev.Result, error) {
	close(a.started)
	select {
	case <-a.release:
		return jev.Result{Pattern: "latency_degradation", Direction: "latency", Model: "fake", EvidenceProbability: 0.8}, nil
	case <-ctx.Done():
		return jev.Result{}, ctx.Err()
	}
}
func TestWorkerProcessesQueuedJobWithoutBlockingCaller(t *testing.T) {
	db, err := storesqlite.Open(filepath.Join(t.TempDir(), "worker.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := anomalysqlite.New(db)
	client := &blockingAnalyst{started: make(chan struct{}), release: make(chan struct{})}
	w := NewWorker(repo, client, nil)
	sink := &eventSink{events: make(chan domain.Anomaly, 1)}
	w.SetBroadcaster(sink)
	w.Start()
	defer w.Stop()
	a := domain.Anomaly{Type: "high_latency", Severity: "high", Message: "slow", DetectedAt: time.Now()}
	if err = repo.SaveWithAnalysis(context.Background(), "p", &a, domain.Snapshot{}, 10); err != nil {
		t.Fatal(err)
	}
	w.Notify()
	select {
	case <-client.started:
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not start")
	}
	list, err := repo.List(context.Background(), "p", 10)
	if err != nil || len(list) != 1 || list[0].AnalysisStatus != "pending" {
		t.Fatalf("alert unavailable while provider blocks: %+v %v", list, err)
	}
	close(client.release)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		list, err = repo.List(context.Background(), "p", 10)
		if err == nil && len(list) == 1 && list[0].AnalysisStatus == "completed" {
			select {
			case event := <-sink.events:
				if event.Diagnosis == nil {
					t.Fatal("missing broadcast diagnosis")
				}
				return
			case <-time.After(time.Second):
				t.Fatal("no update broadcast")
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("job did not complete: %+v %v", list, err)
}
