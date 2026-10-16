package metrics

import (
	"context"
	"time"
)

// Run periodically calculates metrics and sends snapshots to output.
// The caller can stop it by cancelling ctx.
func Run(ctx context.Context, buffer *Buffer, windowSeconds int, interval time.Duration, output chan<- Snapshot) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return

		case now := <-ticker.C:
			records := buffer.Window(now)
			snapshot := Compute(records, windowSeconds)

			select {
			case output <- snapshot:
			case <-ctx.Done():
				return
			}
		}
	}
}
