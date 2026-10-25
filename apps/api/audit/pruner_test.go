package audit_test

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"traffic-center/audit"
	"traffic-center/domain"
)

func TestPrunerPruneOnceRemovesOnlyExpiredEvents(t *testing.T) {
	repo := &fakeRepo{}
	pruner := audit.NewPruner(repo, time.Hour, 24*time.Hour, slog.New(slog.NewTextHandler(io.Discard, nil)))

	now := time.Now().UTC()
	repo.recorded = []domain.AuditEvent{
		{Ts: now.Add(-48 * time.Hour), Action: domain.AuditAuthDenied, Outcome: domain.AuditOutcomeFailure},
		{Ts: now.Add(-2 * time.Hour), Action: domain.AuditProjectCreate, Outcome: domain.AuditOutcomeSuccess},
	}

	pruned, err := pruner.PruneOnce(context.Background())
	if err != nil {
		t.Fatalf("PruneOnce: %v", err)
	}
	if pruned != 1 {
		t.Fatalf("pruned = %d, want 1", pruned)
	}
	if len(repo.recorded) != 1 || repo.recorded[0].Action != domain.AuditProjectCreate {
		t.Fatalf("remaining events = %+v, want only the recent one", repo.recorded)
	}
}

func TestPrunerZeroRetentionIsDisabled(t *testing.T) {
	repo := &fakeRepo{}
	pruner := audit.NewPruner(repo, time.Hour, 0, nil)

	repo.recorded = []domain.AuditEvent{
		{Ts: time.Now().Add(-1000 * time.Hour), Action: domain.AuditProjectDelete, Outcome: domain.AuditOutcomeSuccess},
	}

	pruned, err := pruner.PruneOnce(context.Background())
	if err != nil {
		t.Fatalf("PruneOnce: %v", err)
	}
	if pruned != 0 {
		t.Fatalf("pruned = %d, want 0 (retention disabled)", pruned)
	}
	if len(repo.recorded) != 1 {
		t.Fatalf("events = %d, want 1 (nothing removed)", len(repo.recorded))
	}
}
