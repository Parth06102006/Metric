package memory

import (
	"context"
	"reflect"
	"testing"
	"time"

	"traffic-center/domain"
)

func TestActiveProjectsPreservesLiveRecordsWithoutDuplicates(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	expiredA := domain.LogRecord{Ts: now.Add(-2 * time.Hour), Endpoint: "/expired-a"}
	liveA := domain.LogRecord{Ts: now.Add(-time.Minute), Endpoint: "/live-a"}
	expiredB := domain.LogRecord{Ts: now.Add(-3 * time.Hour), Endpoint: "/expired-b"}
	liveB := domain.LogRecord{Ts: now, Endpoint: "/live-b"}
	appended := domain.LogRecord{Ts: now, Endpoint: "/appended"}

	for _, tc := range []struct {
		name   string
		calls  int
		append bool
	}{
		{name: "interleaved records", calls: 1},
		{name: "repeated calls", calls: 3},
		{name: "append after pruning", calls: 1, append: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := New(time.Hour)
			if err := r.Append(ctx, "project", []domain.LogRecord{expiredA, liveA, expiredB, liveB}); err != nil {
				t.Fatal(err)
			}
			if err := r.Append(ctx, "expired", []domain.LogRecord{expiredA}); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < tc.calls; i++ {
				if got := r.ActiveProjects(ctx); !reflect.DeepEqual(got, []string{"project"}) {
					t.Fatalf("ActiveProjects call %d = %v, want [project]", i+1, got)
				}
			}
			want := []domain.LogRecord{liveA, liveB}
			if tc.append {
				if err := r.Append(ctx, "project", []domain.LogRecord{appended}); err != nil {
					t.Fatal(err)
				}
				want = append(want, appended)
			}
			got, err := r.Recent(ctx, "project", now.Add(-time.Hour))
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("Recent = %#v, want %#v", got, want)
			}
		})
	}
}
