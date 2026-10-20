package anomaly

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"traffic-center/domain"
)

// Worker periodically snapshots every active project, persists the snapshot,
// broadcasts it, and hands it to the anomaly evaluator. It also prunes old
// snapshots so the database does not grow without bound.
type Worker struct {
	recorder    domain.RecordRepository
	snapshots   domain.SnapshotProvider
	store       domain.SnapshotRepository
	evaluator   *Usecase
	broadcaster domain.Broadcaster
	interval    time.Duration
	retention   time.Duration
	logger      *slog.Logger

	stop      chan struct{}
	stopOnce  sync.Once
	wg        sync.WaitGroup
	lastPrune time.Time
}

// NewWorker wires the evaluation loop.
func NewWorker(
	recorder domain.RecordRepository,
	snapshots domain.SnapshotProvider,
	store domain.SnapshotRepository,
	evaluator *Usecase,
	broadcaster domain.Broadcaster,
	interval, retention time.Duration,
	logger *slog.Logger,
) *Worker {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Worker{
		recorder:    recorder,
		snapshots:   snapshots,
		store:       store,
		evaluator:   evaluator,
		broadcaster: broadcaster,
		interval:    interval,
		retention:   retention,
		logger:      logger,
		stop:        make(chan struct{}),
	}
}

// Start launches the loop in a background goroutine.
func (w *Worker) Start() {
	w.wg.Add(1)
	go w.run()
}

// Stop is idempotent and waits for the current cycle.
func (w *Worker) Stop() {
	w.stopOnce.Do(func() { close(w.stop) })
	w.wg.Wait()
}

func (w *Worker) run() {
	defer w.wg.Done()
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	for {
		select {
		case <-w.stop:
			return
		case <-ticker.C:
			w.EvaluateAll(context.Background())
		}
	}
}

// EvaluateAll runs one pass over every project that has received traffic.
func (w *Worker) EvaluateAll(ctx context.Context) {
	if w.recorder == nil || w.snapshots == nil {
		return
	}

	for _, projectID := range w.recorder.ActiveProjects(ctx) {
		select {
		case <-ctx.Done():
			return
		case <-w.stop:
			return
		default:
			w.EvaluateProject(ctx, projectID)
		}
	}

	if w.retention > 0 && w.store != nil && (w.lastPrune.IsZero() || time.Since(w.lastPrune) >= time.Hour) {
		pruned, err := w.store.Prune(ctx, time.Now().Add(-w.retention))
		if err != nil {
			w.logger.Error("prune snapshots", "error", err)
		} else if pruned > 0 {
			w.logger.Info("pruned snapshots", "rows", pruned)
		}
		w.lastPrune = time.Now()
	}
}

// EvaluateProject computes one snapshot and drives the pipeline for it.
func (w *Worker) EvaluateProject(ctx context.Context, projectID string) {
	snapshot, err := w.snapshots.Current(ctx, projectID)
	if err != nil {
		w.logger.Error("compute snapshot", "project_id", projectID, "error", err)
		return
	}

	if w.store != nil {
		if err := w.store.Save(ctx, projectID, snapshot); err != nil {
			w.logger.Error("save snapshot", "project_id", projectID, "error", err)
		}
	}

	if w.broadcaster != nil {
		w.broadcaster.BroadcastSnapshot(projectID, snapshot)
	}

	if w.evaluator != nil {
		if err := w.evaluator.Evaluate(ctx, projectID, snapshot); err != nil {
			w.logger.Error("evaluate anomalies", "project_id", projectID, "error", err)
		}
	}
}
