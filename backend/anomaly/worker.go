package anomaly

import (
	"context"
	"log"
	"sync"
	"time"

	"traffic-center/backend/llm"
	"traffic-center/backend/metrics"
	"traffic-center/backend/store"
	"traffic-center/backend/ws"
)

// IngestTracker provides access to buffers and active projects.
type IngestTracker interface {
	ActiveProjects() []string
	GetBuffer(projectID string) (*metrics.Buffer, bool)
}

// AnomalyExplainer defines the interface for generating anomaly explanations.
type AnomalyExplainer interface {
	Explain(ctx context.Context, projectID string, anom llm.AnomalyInfo, snap metrics.Snapshot) string
}

// EventBroadcaster defines the interface to broadcast events to websocket clients.
type EventBroadcaster interface {
	BroadcastSnapshot(projectID string, snapshot metrics.Snapshot)
	BroadcastAnomaly(projectID string, anom ws.AnomalyPayload)
}

// PipelineWorker coordinates snapshot collection, anomaly detection, deduplication, and persistence.
type PipelineWorker struct {
	Store         *store.Store
	Ingest        IngestTracker
	Explainer     AnomalyExplainer
	Broadcaster   EventBroadcaster
	Interval      time.Duration
	WindowSeconds int
	DedupeWindow  time.Duration
	stopCh        chan struct{}
	wg            sync.WaitGroup
}

// NewWorker initializes a new pipeline evaluation worker.
func NewWorker(s *store.Store, ingest IngestTracker, explainer AnomalyExplainer, broadcaster EventBroadcaster, interval time.Duration) *PipelineWorker {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	return &PipelineWorker{
		Store:         s,
		Ingest:        ingest,
		Explainer:     explainer,
		Broadcaster:   broadcaster,
		Interval:      interval,
		WindowSeconds: 60,
		DedupeWindow:  60 * time.Second,
		stopCh:        make(chan struct{}),
	}
}

// Start launches the evaluation worker in a background goroutine.
func (w *PipelineWorker) Start() {
	w.wg.Add(1)
	go w.run()
}

// Stop gracefully stops the evaluation worker and waits for current cycle completion.
func (w *PipelineWorker) Stop() {
	close(w.stopCh)
	w.wg.Wait()
}

func (w *PipelineWorker) run() {
	defer w.wg.Done()
	ticker := time.NewTicker(w.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-w.stopCh:
			return
		case <-ticker.C:
			w.EvaluateAll(context.Background())
		}
	}
}

// EvaluateAll runs one evaluation pass across all active projects.
func (w *PipelineWorker) EvaluateAll(ctx context.Context) {
	if w.Ingest == nil || w.Store == nil {
		return
	}

	projectIDs := w.Ingest.ActiveProjects()
	for _, projectID := range projectIDs {
		select {
		case <-ctx.Done():
			return
		case <-w.stopCh:
			return
		default:
			w.EvaluateProject(projectID)
		}
	}
}

// EvaluateProject runs anomaly evaluation and persistence for a single project.
func (w *PipelineWorker) EvaluateProject(projectID string) {
	buffer, ok := w.Ingest.GetBuffer(projectID)
	if !ok || buffer == nil {
		return
	}

	records := buffer.Window(time.Now())
	snapshot := metrics.Compute(records, w.WindowSeconds)

	// Persist snapshot to database
	if err := w.Store.SaveSnapshot(projectID, snapshot); err != nil {
		log.Printf("worker: error saving snapshot for %s: %v", projectID, err)
	}

	// Broadcast periodic metric snapshot to connected WebSocket clients
	if w.Broadcaster != nil {
		w.Broadcaster.BroadcastSnapshot(projectID, snapshot)
	}

	// Fetch thresholds configured for the project
	dbThresholds, err := w.Store.GetThresholds(projectID)
	if err != nil {
		return
	}

	detectorThresholds := Thresholds{
		MaxRPS:       dbThresholds.RPSMax,
		MaxErrorRate: dbThresholds.ErrorRate,
		MaxP95:       dbThresholds.P95Ms,
		MaxTopIPRPS:  dbThresholds.ReqPerIPMax,
	}

	detected := Detect(snapshot, detectorThresholds)
	for _, a := range detected {
		// Deduplication check: only record and broadcast if not recently reported within DedupeWindow
		recent, err := w.Store.HasRecentAnomaly(projectID, a.Type, w.DedupeWindow)
		if err != nil {
			log.Printf("worker: dedupe check error for %s (%s): %v", projectID, a.Type, err)
			continue
		}
		if recent {
			// Ongoing anomaly: do not insert repeated records or spam clients
			continue
		}

		var analysis string
		if w.Explainer != nil {
			analysis = w.Explainer.Explain(context.Background(), projectID, llm.AnomalyInfo{
				Type:      a.Type,
				Severity:  a.Severity,
				Message:   a.Message,
				Value:     a.Value,
				Threshold: a.Threshold,
			}, snapshot)
		}

		if err := w.Store.RecordAnomaly(projectID, store.AnomalyRecord{
			Type:       a.Type,
			Severity:   a.Severity,
			Message:    a.Message,
			Value:      a.Value,
			Threshold:  a.Threshold,
			Analysis:   analysis,
			DetectedAt: a.DetectedAt,
		}); err != nil {
			log.Printf("worker: error recording anomaly for %s: %v", projectID, err)
			continue
		}

		// Broadcast new anomaly alert event to connected clients
		if w.Broadcaster != nil {
			w.Broadcaster.BroadcastAnomaly(projectID, ws.AnomalyPayload{
				Type:       a.Type,
				Severity:   a.Severity,
				Message:    a.Message,
				Value:      a.Value,
				Threshold:  a.Threshold,
				Analysis:   analysis,
				DetectedAt: a.DetectedAt,
			})
		}
	}
}
