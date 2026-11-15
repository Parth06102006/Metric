package memory

import (
	"context"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"traffic-center/domain"
)

// Repository keeps exact live records. The map lock protects only entry lookup;
// copying/pruning one project never holds it or locks another project's window.
type Repository struct {
	mu           sync.RWMutex
	window       time.Duration
	projects     map[string]*projectWindow
	limits       Limits
	budgetMu     sync.Mutex
	totalRecords int
	totalBytes   int64
}

type projectWindow struct {
	mu              sync.Mutex
	records         []domain.LogRecord
	earliest        time.Time
	latest          atomic.Int64 // newest event timestamp, for nonblocking active discovery
	retired         bool
	nextAppendPrune time.Time
	owner           *Repository
	bytes           int64
	endpoints       map[string]int
}

// Limits bound retained telemetry, not total process RSS (copies/GC add overhead).
type Limits struct {
	ProjectRecords, TotalRecords, Projects int
	ProjectEndpoints                       int
	ProjectBytes, TotalBytes               int64
}

func DefaultLimits() Limits {
	return Limits{ProjectRecords: 50000, TotalRecords: 200000, Projects: 512, ProjectEndpoints: 512, ProjectBytes: 16 << 20, TotalBytes: 64 << 20}
}

func New(window time.Duration) *Repository { return NewWithLimits(window, DefaultLimits()) }

func NewWithLimits(window time.Duration, limits Limits) *Repository {
	if window <= 0 {
		window = time.Minute
	}
	defaults := DefaultLimits()
	if limits.ProjectRecords <= 0 {
		limits.ProjectRecords = defaults.ProjectRecords
	}
	if limits.TotalRecords <= 0 {
		limits.TotalRecords = defaults.TotalRecords
	}
	if limits.Projects <= 0 {
		limits.Projects = defaults.Projects
	}
	if limits.ProjectEndpoints <= 0 {
		limits.ProjectEndpoints = defaults.ProjectEndpoints
	}
	if limits.ProjectBytes <= 0 {
		limits.ProjectBytes = defaults.ProjectBytes
	}
	if limits.TotalBytes <= 0 {
		limits.TotalBytes = defaults.TotalBytes
	}
	return &Repository{window: window, projects: make(map[string]*projectWindow), limits: limits}
}

func (r *Repository) lookup(id string, create bool) *projectWindow {
	r.mu.RLock()
	p := r.projects[id]
	r.mu.RUnlock()
	if p != nil || !create {
		return p
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if p = r.projects[id]; p == nil {
		if len(r.projects) >= r.limits.Projects {
			return nil
		}
		p = &projectWindow{owner: r}
		r.projects[id] = p
	}
	return p
}

func (r *Repository) Append(ctx context.Context, id string, records []domain.LogRecord) error {
	return r.AppendCommitted(ctx, id, records, nil)
}

// AppendCommitted admits the complete batch and invokes commit while the project
// window is locked, before publishing it to readers. A failed commit rolls back
// the in-memory reservation. The callback must not call this repository.
func (r *Repository) AppendCommitted(ctx context.Context, id string, records []domain.LogRecord, commit func([]domain.LogRecord) error) error {
	// Validate time at this boundary too, so direct adapters cannot retain a
	// future event indefinitely. The HTTP usecase provides stricter field checks.
	now := time.Now()
	for _, record := range records {
		if record.Ts.After(now.Add(5 * time.Second)) {
			return domain.Validationf("timestamp exceeds future skew allowance")
		}
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		p := r.lookup(id, true)
		if p == nil {
			return domain.ErrCapacity
		}
		p.mu.Lock()
		if p.retired {
			p.mu.Unlock()
			continue
		}
		now := time.Now()
		cutoff := now.Add(-r.window)
		// A record can expire while waiting for the project lock. Reject the
		// whole batch rather than acknowledge a record that was never retained.
		for _, record := range records {
			if record.Ts.Before(cutoff) {
				if len(p.records) == 0 {
					r.retire(id, p)
				}
				p.mu.Unlock()
				return domain.Validationf("log record timestamp has expired from the live window")
			}
		}
		// Amortize expiry sweeps under sustained ingestion. Read paths still
		// prune at their exact cutoff, so metrics never include expired records.
		if !now.Before(p.nextAppendPrune) {
			p.prune(cutoff)
			p.nextAppendPrune = now.Add(min(time.Second, r.window))
		}
		count, size := 0, int64(0)
		newEndpoints := make(map[string]struct{})
		for _, record := range records {
			if !record.Ts.Before(cutoff) {
				count++
				size += recordBytes(record)
				if p.endpoints[record.Endpoint] == 0 {
					newEndpoints[record.Endpoint] = struct{}{}
				}
			}
		}
		if count == 0 {
			if len(p.records) == 0 {
				r.retire(id, p)
			}
			p.mu.Unlock()
			return nil
		}
		// Refresh expired accounting on capacity pressure even if the normal
		// append sweep is deferred. Admission is atomic for the entire batch.
		if len(p.records)+count > r.limits.ProjectRecords || p.bytes+size > r.limits.ProjectBytes || len(p.endpoints)+len(newEndpoints) > r.limits.ProjectEndpoints {
			p.prune(cutoff)
			clear(newEndpoints)
			for _, record := range records {
				if !record.Ts.Before(cutoff) && p.endpoints[record.Endpoint] == 0 {
					newEndpoints[record.Endpoint] = struct{}{}
				}
			}
		}
		r.budgetMu.Lock()
		allowed := len(p.records)+count <= r.limits.ProjectRecords && len(p.endpoints)+len(newEndpoints) <= r.limits.ProjectEndpoints && p.bytes+size <= r.limits.ProjectBytes && r.totalRecords+count <= r.limits.TotalRecords && r.totalBytes+size <= r.limits.TotalBytes
		if allowed {
			r.totalRecords += count
			r.totalBytes += size
			p.bytes += size
		}
		r.budgetMu.Unlock()
		if !allowed {
			if len(p.records) == 0 {
				r.retire(id, p)
			}
			p.mu.Unlock()
			return domain.ErrCapacity
		}
		batch := make([]domain.LogRecord, len(records))
		copy(batch, records)
		for i := range batch {
			if batch[i].Ts.After(now) {
				batch[i].Ts = now
			}
		}
		if commit != nil {
			if err := commit(batch); err != nil {
				p.release(count, size)
				if len(p.records) == 0 {
					r.retire(id, p)
				}
				p.mu.Unlock()
				return err
			}
		}
		latest := p.latest.Load()
		if p.endpoints == nil {
			p.endpoints = make(map[string]int)
		}
		for _, record := range batch {
			if p.earliest.IsZero() || record.Ts.Before(p.earliest) {
				p.earliest = record.Ts
			}
			latest = max(latest, record.Ts.UnixNano())
			p.endpoints[record.Endpoint]++
			p.records = append(p.records, record)
		}
		p.latest.Store(latest)
		p.mu.Unlock()
		return nil
	}
}

func (r *Repository) Recent(ctx context.Context, id string, since time.Time) ([]domain.LogRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p := r.lookup(id, false)
	if p == nil {
		return []domain.LogRecord{}, nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.prune(time.Now().Add(-r.window))
	result := make([]domain.LogRecord, 0, len(p.records))
	for _, record := range p.records {
		if !record.Ts.Before(since) {
			result = append(result, record)
		}
	}
	return result, nil
}

func (r *Repository) ActiveProjects(ctx context.Context) []string {
	r.mu.RLock()
	entries := make(map[string]*projectWindow, len(r.projects))
	for id, p := range r.projects {
		entries[id] = p
	}
	r.mu.RUnlock()
	cutoff := time.Now().Add(-r.window)
	ids := make([]string, 0, len(entries))
	for id, p := range entries {
		if ctx.Err() != nil {
			break
		}
		if p.latest.Load() >= cutoff.UnixNano() {
			ids = append(ids, id)
			continue
		}
		// Expired idle entries can be reclaimed without waiting behind a hot
		// project's copy/append. Recheck after locking: append may have refreshed it.
		if p.mu.TryLock() {
			if p.latest.Load() < cutoff.UnixNano() {
				r.retire(id, p)
			} else {
				ids = append(ids, id)
			}
			p.mu.Unlock()
		} else {
			// Conservatively include a concurrently modified entry, never lose traffic.
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

// retire requires p.mu. No code holds the map lock while acquiring p.mu.
func (r *Repository) retire(id string, p *projectWindow) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.projects[id] != p {
		return
	}
	delete(r.projects, id)
	p.retired = true
	p.release(len(p.records), p.bytes)
	p.records = nil
	p.endpoints = nil
	p.earliest = time.Time{}
	p.latest.Store(0)
}

func (r *Repository) Delete(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p := r.lookup(id, false)
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	r.retire(id, p)
	return nil
}

// recordBytes includes a conservative per-record allocation allowance plus strings.
func recordBytes(r domain.LogRecord) int64 {
	return 192 + int64(len(r.Endpoint)+len(r.Method)+len(r.IP))
}

// release requires p.mu; budgetMu is never held while acquiring project locks.
func (p *projectWindow) release(count int, bytes int64) {
	if p.owner != nil {
		p.owner.budgetMu.Lock()
		p.owner.totalRecords -= count
		p.owner.totalBytes -= bytes
		p.owner.budgetMu.Unlock()
	}
	p.bytes -= bytes
}

func (p *projectWindow) prune(cutoff time.Time) {
	// Most batches arrive before any retained event expires. Avoid an O(window)
	// compaction for every append; arbitrary out-of-order records remain supported.
	if p.earliest.IsZero() || !p.earliest.Before(cutoff) {
		return
	}
	oldCount, oldBytes := len(p.records), p.bytes
	var keptBytes int64
	active := p.records[:0]
	var earliest time.Time
	for _, record := range p.records {
		if !record.Ts.Before(cutoff) {
			active = append(active, record)
			keptBytes += recordBytes(record)
			if earliest.IsZero() || record.Ts.Before(earliest) {
				earliest = record.Ts
			}
		} else {
			p.endpoints[record.Endpoint]--
			if p.endpoints[record.Endpoint] == 0 {
				delete(p.endpoints, record.Endpoint)
			}
		}
	}
	p.release(oldCount-len(active), oldBytes-keptBytes)
	clear(p.records[len(active):]) // release expired strings in the backing array
	p.records = active
	// Expired records must not leave a huge backing array pinned by one live
	// record after their global budget is released.
	if len(active) > 0 && cap(active) > max(64, 2*len(active)) {
		p.records = append([]domain.LogRecord(nil), active...)
	}
	p.earliest = earliest
	if len(active) == 0 {
		p.records = nil
		p.latest.Store(0)
	}
}
