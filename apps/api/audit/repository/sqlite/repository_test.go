package sqlite_test

import (
	"context"
	"strings"
	"testing"
	"time"

	auditsqlite "traffic-center/audit/repository/sqlite"
	"traffic-center/domain"
	"traffic-center/internal/sqlite"
)

func TestAuditRepositoryRecordListPrune(t *testing.T) {
	db, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer db.Close()

	repo := auditsqlite.New(db)
	ctx := context.Background()

	secretKey := "sk-live-0000000000000000000000000000deadbeef"
	old := domain.AuditEvent{
		Ts:              time.Now().Add(-200 * time.Hour).UTC(),
		// Real callers (middleware helpers, usecase) sanitize the actor before
		// the repository; the repo stores the actor verbatim.
		ActorEmail:      "intruder@example.com",
		Action:          domain.AuditAccessDenied,
		TargetProjectID: "p-old",
		Outcome:         domain.AuditOutcomeFailure,
		RequestID:       "req-old",
		Details:         map[string]string{"reason": "project_access_denied", "presented_key": secretKey},
	}
	recent := domain.AuditEvent{
		Ts:              time.Now().UTC(),
		ActorEmail:      "admin@example.com",
		Action:          domain.AuditProjectCreate,
		TargetProjectID: "p-new",
		Outcome:         domain.AuditOutcomeSuccess,
		RequestID:       "req-new",
	}
	if err := repo.Record(ctx, old); err != nil {
		t.Fatalf("record old: %v", err)
	}
	if err := repo.Record(ctx, recent); err != nil {
		t.Fatalf("record recent: %v", err)
	}

	events, err := repo.List(ctx, 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("events = %d, want 2", len(events))
	}

	// Newest first.
	if events[0].Action != domain.AuditProjectCreate || events[0].TargetProjectID != "p-new" {
		t.Fatalf("newest event = %+v, want the recent create", events[0])
	}

	// Stored actor is preserved verbatim; sensitive details never reach the table.
	denied := events[1]
	if denied.ActorEmail != "intruder@example.com" {
		t.Fatalf("actor = %q, want stored verbatim", denied.ActorEmail)
	}
	if denied.Details["presented_key"] != "[redacted]" {
		t.Fatalf("presented_key = %q, want [redacted]", denied.Details["presented_key"])
	}
	if denied.Ts.Location() != time.UTC {
		t.Fatalf("ts = %v, want UTC", denied.Ts)
	}

	pruned, err := repo.Prune(ctx, time.Now().Add(-100*time.Hour))
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if pruned != 1 {
		t.Fatalf("pruned = %d, want 1", pruned)
	}

	events, err = repo.List(ctx, 10)
	if err != nil {
		t.Fatalf("list after prune: %v", err)
	}
	if len(events) != 1 || events[0].Action != domain.AuditProjectCreate {
		t.Fatalf("events after prune = %+v, want only the recent create", events)
	}
	if strings.Contains(serializeEvents(t, events), secretKey) {
		t.Fatal("stored audit rows leaked the presented key")
	}
}

func serializeEvents(t *testing.T, events []domain.AuditEvent) string {
	t.Helper()
	encoded := ""
	for _, event := range events {
		for _, value := range event.Details {
			encoded += value + "\n"
		}
	}
	return encoded
}
