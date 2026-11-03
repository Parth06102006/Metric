package audit_test

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"traffic-center/audit"
	"traffic-center/audit/repository/memory"
	"traffic-center/domain"
)

func TestPrunerPruneOnceRemovesOnlyExpiredEvents(t *testing.T) {
	repo := memory.New()
	pruner := audit.NewPruner(repo, time.Hour, 24*time.Hour, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx := context.Background()

	now := time.Now().UTC()
	if err := repo.Record(ctx, domain.AuditEvent{Ts: now.Add(-48 * time.Hour), Action: domain.AuditAuthDenied, Outcome: domain.AuditOutcomeFailure}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if err := repo.Record(ctx, domain.AuditEvent{Ts: now.Add(-2 * time.Hour), Action: domain.AuditProjectCreate, Outcome: domain.AuditOutcomeSuccess}); err != nil {
		t.Fatalf("Record: %v", err)
	}

	pruned, err := pruner.PruneOnce(ctx)
	if err != nil {
		t.Fatalf("PruneOnce: %v", err)
	}
	if pruned != 1 {
		t.Fatalf("pruned = %d, want 1", pruned)
	}

	remaining, err := repo.List(ctx, domain.AuditQuery{Limit: 10})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(remaining) != 1 || remaining[0].Action != domain.AuditProjectCreate {
		t.Fatalf("remaining events = %+v, want only the recent one", remaining)
	}
}

func TestPrunerZeroRetentionIsDisabled(t *testing.T) {
	repo := memory.New()
	pruner := audit.NewPruner(repo, time.Hour, 0, nil)
	ctx := context.Background()

	if err := repo.Record(ctx, domain.AuditEvent{Ts: time.Now().Add(-1000 * time.Hour), Action: domain.AuditProjectDelete, Outcome: domain.AuditOutcomeSuccess}); err != nil {
		t.Fatalf("Record: %v", err)
	}

	pruned, err := pruner.PruneOnce(ctx)
	if err != nil {
		t.Fatalf("PruneOnce: %v", err)
	}
	if pruned != 0 {
		t.Fatalf("pruned = %d, want 0 (retention disabled)", pruned)
	}

	remaining, err := repo.List(ctx, domain.AuditQuery{Limit: 10})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(remaining) != 1 {
		t.Fatalf("events = %d, want 1 (nothing removed)", len(remaining))
	}
}
