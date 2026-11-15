package ws

import (
	"context"
	"testing"
	"time"
)

func TestInitialSnapshotAdmissionIsBounded(t *testing.T) {
	h := NewHandler(nil, nil, nil, time.Minute, nil, nil, nil)
	for i := 0; i < cap(h.initialSlots); i++ {
		h.initialSlots <- struct{}{}
	}
	if h.sendInitialSnapshot(context.Background(), nil, "p", time.Now().Add(time.Second)) {
		t.Fatal("accepted initial snapshot over capacity")
	}
}
