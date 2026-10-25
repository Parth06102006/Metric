package audit

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"traffic-center/domain"
)

// defaultPruneInterval is how often the retention pass runs. Pruning is cheap
// and infrequent; hourly matches the snapshot retention cadence.
const defaultPruneInterval = time.Hour

// Pruner periodically removes audit events older than the retention window so
// the audit_events table does not grow without bound. It mirrors the
// snapshot-pruning design of the anomaly worker: a background loop with an
// idempotent Stop.
//
// Durability note: retention deletes aged rows by design. The documented
// write-failure policy (failed audit writes are lost, loudly logged at ERROR)
// is unchanged; the pruner neither retries nor queues.
type Pruner struct {
	repo      domain.AuditRepository
	retention time.Duration
	interval  time.Duration
	logger    *slog.Logger

	stop     chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup
}

// NewPruner wires the retention loop. A retention of zero or less disables
// pruning entirely; interval of zero or less uses the hourly default.
func NewPruner(repo domain.AuditRepository, interval, retention time.Duration, logger *slog.Logger) *Pruner {
	if logger == nil {
		logger = slog.Default()
	}
	if interval <= 0 {
		interval = defaultPruneInterval
	}
	return &Pruner{
		repo:      repo,
		retention: retention,
		interval:  interval,
		logger:    logger,
		stop:      make(chan struct{}),
	}
}

// Start launches the retention loop in a background goroutine.
func (p *Pruner) Start() {
	p.wg.Add(1)
	go p.run()
}

// Stop is idempotent and waits for the current pass to finish.
func (p *Pruner) Stop() {
	p.stopOnce.Do(func() { close(p.stop) })
	p.wg.Wait()
}

func (p *Pruner) run() {
	defer p.wg.Done()
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()

	for {
		select {
		case <-p.stop:
			return
		case <-ticker.C:
			if _, err := p.PruneOnce(context.Background()); err != nil {
				p.logger.Error("prune audit events", "error", err)
			}
		}
	}
}

// PruneOnce removes audit events older than the retention window and returns
// the number of rows deleted. It is exported for tests and manual operation.
func (p *Pruner) PruneOnce(ctx context.Context) (int64, error) {
	if p.repo == nil || p.retention <= 0 {
		return 0, nil
	}
	return p.repo.Prune(ctx, time.Now().Add(-p.retention))
}
