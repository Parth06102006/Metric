// Package analysis runs durable, advisory incident analyses in the background.
package analysis

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	anomalysqlite "traffic-center/anomaly/repository/sqlite"
	"traffic-center/domain"
	"traffic-center/internal/jev"
)

type analyst interface {
	Diagnose(context.Context, jev.State) (jev.Result, error)
}
type Worker struct {
	repo      *anomalysqlite.Repository
	analyst   analyst
	snapshots domain.SnapshotRepository
	wake      chan struct{}
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	logger    *slog.Logger
	updates   domain.Broadcaster
}

func NewWorker(repo *anomalysqlite.Repository, analyst analyst, snapshots domain.SnapshotRepository) *Worker {
	return &Worker{repo: repo, analyst: analyst, snapshots: snapshots, wake: make(chan struct{}, 1), logger: slog.Default()}
}
func (w *Worker) SetBroadcaster(b domain.Broadcaster) { w.updates = b }
func (w *Worker) Start() {
	ctx, cancel := context.WithCancel(context.Background())
	w.cancel = cancel
	w.wg.Add(1)
	go func() { defer w.wg.Done(); w.run(ctx) }()
}
func (w *Worker) Stop() {
	if w.cancel != nil {
		w.cancel()
		w.wg.Wait()
	}
}
func (w *Worker) Notify() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}
func (w *Worker) run(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	var lastCall time.Time
	for {
		for ctx.Err() == nil {
			job, err := w.repo.ClaimAnalysis(ctx, time.Now(), time.Minute)
			if errors.Is(err, sql.ErrNoRows) {
				break
			}
			if err != nil {
				if ctx.Err() == nil {
					w.logger.Error("claim analysis job", "error", err)
				}
				break
			}
			if job.Attempts > 3 {
				w.fail(ctx, job, true)
				continue
			}
			// Bound spending even while many projects produce incidents.
			if !lastCall.IsZero() {
				wait := time.Until(lastCall.Add(30 * time.Second))
				if wait > 0 {
					timer := time.NewTimer(wait)
					select {
					case <-ctx.Done():
						timer.Stop()
						w.release(job, false)
						return
					case <-timer.C:
					}
				}
			}
			lastCall = time.Now()
			w.process(ctx, job)
		}
		select {
		case <-ctx.Done():
			return
		case <-w.wake:
		case <-ticker.C:
		}
	}
}
func (w *Worker) process(ctx context.Context, job anomalysqlite.AnalysisJob) {
	if ctx.Err() != nil {
		w.release(job, false)
		return
	}
	var state jev.State
	if err := json.Unmarshal([]byte(job.StateJSON), &state); err != nil {
		w.fail(ctx, job, true)
		return
	}
	if w.snapshots != nil {
		observedAt := job.CreatedAt
		if state.ObservedAt != nil {
			observedAt = *state.ObservedAt
		}
		state.Baseline = w.baseline(ctx, job.ProjectID, observedAt, state.WindowSeconds)
	}
	if ctx.Err() != nil {
		w.release(job, false)
		return
	}
	result, err := w.analyst.Diagnose(ctx, state)
	if err != nil {
		if ctx.Err() != nil {
			// The provider may have received and billed the request.
			w.release(job, true)
			return
		}
		w.logger.Warn("incident analysis failed", "project_id", job.ProjectID, "anomaly_id", job.AnomalyID, "attempt", job.Attempts, "error", err)
		w.fail(ctx, job, job.Attempts >= 3)
		return
	}

	d := domain.Diagnosis{Pattern: result.Pattern, Direction: result.Direction, Urgency: result.Urgency, PatternConfidence: result.PatternConfidence, DirectionConfidence: result.DirectionConfidence, UrgencyConfidence: result.UrgencyConfidence, EvidenceProbability: result.EvidenceProbability, Model: result.Model, InputTokens: result.InputTokens, Evidence: &state}
	narrative := "Advisory analysis: insufficient evidence; inspect the observed metrics."
	endpointSupported := result.Direction != "failing_endpoints" || (state.Endpoints != nil && state.Endpoints.MostErrors > 0)
	if state.Baseline != nil && state.RequestCount != nil && *state.RequestCount >= 20 && result.EvidenceProbability >= 0.7 && result.Pattern != "unclear" && result.Direction != "more_evidence" && endpointSupported {
		d.EvidenceSufficient = true
		narrative = fmt.Sprintf("Advisory pattern: %s. Suggested investigation: %s. Review before acting.", result.Pattern, result.Direction)
	}
	if err := w.repo.CompleteAnalysis(ctx, job, d, narrative); err != nil {
		if ctx.Err() == nil {
			w.logger.Error("complete analysis job", "anomaly_id", job.AnomalyID, "error", err)
		}
		return
	}
	w.broadcast(ctx, job)
}
func (w *Worker) release(job anomalysqlite.AnalysisJob, attempted bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var err error
	if attempted {
		err = w.repo.FailAnalysis(ctx, job, time.Now(), false)
	} else {
		err = w.repo.ReleaseAnalysis(ctx, job)
	}
	if err != nil {
		w.logger.Warn("release analysis lease", "anomaly_id", job.AnomalyID, "error", err)
	}
}
func (w *Worker) fail(ctx context.Context, job anomalysqlite.AnalysisJob, permanent bool) {
	delay := time.Duration(1<<min(job.Attempts, 6)) * time.Second
	if err := w.repo.FailAnalysis(ctx, job, time.Now().Add(delay), permanent); err != nil {
		if ctx.Err() == nil {
			w.logger.Error("update analysis job failure", "error", err)
		}
		return
	}
	if permanent {
		w.broadcast(ctx, job)
	}
}
func (w *Worker) broadcast(ctx context.Context, job anomalysqlite.AnalysisJob) {
	if w.updates == nil {
		return
	}
	a, err := w.repo.GetAnalysisAnomaly(ctx, job.ProjectID, job.AnomalyID)
	if err == nil {
		w.updates.BroadcastAnomaly(job.ProjectID, a)
	}
}
func (w *Worker) baseline(ctx context.Context, projectID string, observedAt time.Time, windowSeconds int) *jev.Baseline {
	// Do not include snapshots overlapping the incident window. A one-minute
	// minimum also keeps the immediate lead-up out of the comparison.
	gap := max(time.Minute, time.Duration(windowSeconds)*time.Second)
	rows, err := w.snapshots.Range(ctx, projectID, observedAt.Add(-time.Hour), observedAt.Add(-gap), 1000)
	if err != nil || len(rows) < 12 {
		return nil
	}
	// Require at least ten minutes of history; an absent baseline is not zero.
	if rows[len(rows)-1].Ts.Sub(rows[0].Ts) < 10*time.Minute {
		return nil
	}
	r, p, e := make([]float64, 0, len(rows)), make([]float64, 0, len(rows)), make([]float64, 0, len(rows))
	for _, s := range rows {
		r = append(r, s.RPS)
		p = append(p, s.P95)
		e = append(e, s.ErrorRate)
	}
	sort.Float64s(r)
	sort.Float64s(p)
	sort.Float64s(e)
	return &jev.Baseline{Samples: len(rows), Start: rows[0].Ts, End: rows[len(rows)-1].Ts, RPS: r[len(r)/2], P95: p[len(p)/2], ErrorRate: e[len(e)/2]}
}
