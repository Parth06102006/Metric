package metrics

import (
	"context"
	"errors"
	"testing"
	"time"
	"traffic-center/domain"
	"traffic-center/metrics/repository/memory"
)

func TestCurrentBoundsConcurrentComputation(t *testing.T) {
	u := NewUsecase(memory.New(time.Minute), nil, time.Minute)
	for i := 0; i < cap(u.currentSlots); i++ {
		u.currentSlots <- struct{}{}
	}
	if _, err := u.Current(context.Background(), "p"); !errors.Is(err, domain.ErrCapacity) {
		t.Fatalf("expected capacity error: %v", err)
	}
	<-u.currentSlots
	if _, err := u.Current(context.Background(), "p"); err != nil {
		t.Fatalf("slot not reusable: %v", err)
	}
}
