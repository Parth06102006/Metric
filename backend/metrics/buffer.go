package metrics

import (
	"sync"
	"time"
)

// Buffer stores recent request logs for a rolling time window.
type Buffer struct {
	mu      sync.Mutex
	records []LogRecord
	window  time.Duration
}

// NewBuffer creates a buffer with the specified window duration.
func NewBuffer(window time.Duration) *Buffer {
	return &Buffer{
		records: make([]LogRecord, 0),
		window:  window,
	}
}

// Add appends a request record safely.
func (b *Buffer) Add(record LogRecord) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.records = append(b.records, record)
}

// Window removes expired records and returns the remaining records.
func (b *Buffer) Window(now time.Time) []LogRecord {
	b.mu.Lock()
	defer b.mu.Unlock()

	cutoff := now.Add(-b.window)
	active := b.records[:0]

	for _, record := range b.records {
		if !record.Ts.Before(cutoff) {
			active = append(active, record)
		}
	}

	b.records = active

	// Return a copy so callers cannot modify the buffer's slice.
	result := make([]LogRecord, len(active))
	copy(result, active)

	return result
}
