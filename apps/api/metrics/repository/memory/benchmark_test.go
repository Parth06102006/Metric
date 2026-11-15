package memory

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"traffic-center/domain"
)

// Fixed-size windows keep repeated benchmark runs comparable and bound memory.
func BenchmarkLiveWindow(b *testing.B) {
	for _, size := range []int{6000, 60000} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			ctx := context.Background()
			records := make([]domain.LogRecord, size)
			for i := range records {
				records[i] = domain.LogRecord{Ts: time.Now(), Endpoint: fmt.Sprintf("/route/%d", i%20), IP: fmt.Sprintf("10.0.0.%d", i%200), LatencyMs: float64(i % 1000), Status: 200}
			}
			b.Run("reject-expired-batch", func(b *testing.B) {
				r := NewWithLimits(time.Hour, Limits{ProjectRecords: 100000, ProjectBytes: 32 << 20})
				if err := r.Append(ctx, "p", records); err != nil {
					b.Fatal(err)
				}
				batch := make([]domain.LogRecord, 100)
				for i := range batch {
					batch[i] = records[i]
					batch[i].Ts = time.Now().Add(-2 * time.Hour)
				}
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					if err := r.Append(ctx, "p", batch); err == nil {
						b.Fatal("accepted expired batch")
					}
				}
			})
			b.Run("current-exact", func(b *testing.B) {
				r := NewWithLimits(time.Hour, Limits{ProjectRecords: 100000, ProjectBytes: 32 << 20})
				if err := r.Append(ctx, "p", records); err != nil {
					b.Fatal(err)
				}
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					recent, err := r.Recent(ctx, "p", time.Now().Add(-time.Hour))
					if err != nil {
						b.Fatal(err)
					}
					_ = domain.Compute(recent, 3600)
				}
			})
		})
	}
}

// Concurrent readers across eight projects expose global versus project-local
// window locking. Use -cpu=8 for distinct IDs; -cpu=12 also tests shared IDs.
func BenchmarkProjectContention(b *testing.B) {
	r := New(time.Hour)
	ctx := context.Background()
	ids := make([]string, 8)
	records := make([]domain.LogRecord, 6000)
	for i := range records {
		records[i] = domain.LogRecord{Ts: time.Now(), Endpoint: "/x"}
	}
	for i := range ids {
		ids[i] = fmt.Sprint(i)
		if err := r.Append(ctx, ids[i], records); err != nil {
			b.Fatal(err)
		}
	}
	var next atomic.Uint64
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		id := ids[(next.Add(1)-1)%uint64(len(ids))]
		for pb.Next() {
			_, _ = r.Recent(ctx, id, time.Now().Add(-time.Hour))
		}
	})
	b.StopTimer()
}

func TestConcurrentAppendDiscoveryAndDelete(t *testing.T) {
	r := New(time.Minute)
	ctx := context.Background()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := fmt.Sprint(i % 3)
			for j := 0; j < 100; j++ {
				_ = r.Append(ctx, id, []domain.LogRecord{{Ts: time.Now()}})
				_, _ = r.Recent(ctx, id, time.Now().Add(-time.Minute))
				_ = r.ActiveProjects(ctx)
				if j%10 == 0 {
					_ = r.Delete(ctx, id)
				}
			}
		}(i)
	}
	wg.Wait()
	for i := 0; i < 3; i++ {
		_ = r.Delete(ctx, fmt.Sprint(i))
	}
	if len(r.ActiveProjects(ctx)) != 0 {
		t.Fatal("windows not removed")
	}
}
