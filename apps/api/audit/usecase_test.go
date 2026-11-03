package audit_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"traffic-center/audit"
	"traffic-center/audit/repository/memory"
	"traffic-center/domain"
)

// spyRepo records the query the usecase forwarded, so the clamping rules are
// visible without duplicating them in the test.
type spyRepo struct {
	*memory.Repository
	lastQuery domain.AuditQuery
}

func (r *spyRepo) List(ctx context.Context, query domain.AuditQuery) ([]domain.AuditEvent, error) {
	r.lastQuery = query
	return r.Repository.List(ctx, query)
}

// failingRepo fails every write, which is how the non-blocking policy is tested.
type failingRepo struct{}

func (failingRepo) Record(context.Context, domain.AuditEvent) error {
	return errors.New("disk on fire")
}

func (failingRepo) List(context.Context, domain.AuditQuery) ([]domain.AuditEvent, error) {
	return nil, nil
}

func (failingRepo) Prune(context.Context, time.Time) (int64, error) { return 0, nil }

func TestLogStoresRedactedEvent(t *testing.T) {
	repo := memory.New()
	usecase := audit.NewUsecase(repo, nil)

	usecase.Log(context.Background(), "Admin@Example.COM ", domain.AuditProjectCreate, "p1",
		domain.AuditOutcomeSuccess, map[string]string{"project_name": "Checkout", "api_key": "sk-should-not-be-stored"})

	events, err := repo.List(context.Background(), domain.AuditQuery{Limit: 10})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("recorded events = %d, want 1", len(events))
	}
	event := events[0]
	if event.ActorEmail != "admin@example.com" {
		t.Fatalf("actor = %q, want normalized email", event.ActorEmail)
	}
	if event.Details["api_key"] != "[redacted]" {
		t.Fatalf("api_key detail = %q, want [redacted]", event.Details["api_key"])
	}
	if event.Details["project_name"] != "Checkout" {
		t.Fatalf("project_name = %q, want preserved", event.Details["project_name"])
	}
}

func TestLogNeverFailsTheRequestOnWriteError(t *testing.T) {
	var buf bytes.Buffer
	usecase := audit.NewUsecase(failingRepo{}, slog.New(slog.NewTextHandler(&buf, nil)))

	// Must not panic and must emit an ERROR to the application log, the
	// documented non-silent failure channel for audit-write problems.
	usecase.Log(context.Background(), "a@b.c", domain.AuditKeyRotate, "p1", domain.AuditOutcomeSuccess, nil)

	if !strings.Contains(buf.String(), "audit record write failed") {
		t.Fatalf("audit write failure not logged: %q", buf.String())
	}
	if strings.Contains(buf.String(), "sk-") {
		t.Fatal("failure log must not contain credentials")
	}
}

func TestListBoundsLimit(t *testing.T) {
	repo := &spyRepo{Repository: memory.New()}
	usecase := audit.NewUsecase(repo, nil)

	if _, err := usecase.List(context.Background(), domain.AuditQuery{}); err != nil {
		t.Fatalf("List(zero): %v", err)
	}
	if repo.lastQuery.Limit != 100 {
		t.Fatalf("default limit = %d, want 100", repo.lastQuery.Limit)
	}

	if _, err := usecase.List(context.Background(), domain.AuditQuery{Limit: audit.MaxListSize + 1}); err != nil {
		t.Fatalf("List(over max): %v", err)
	}
	if repo.lastQuery.Limit != 100 {
		t.Fatalf("oversized limit = %d, want clamped to 100", repo.lastQuery.Limit)
	}

	if _, err := usecase.List(context.Background(), domain.AuditQuery{Limit: 25}); err != nil {
		t.Fatalf("List(25): %v", err)
	}
	if repo.lastQuery.Limit != 25 {
		t.Fatalf("explicit limit = %d, want 25", repo.lastQuery.Limit)
	}
}

func TestListFiltersByActionProjectAndCursor(t *testing.T) {
	repo := memory.New()
	usecase := audit.NewUsecase(repo, nil)
	ctx := context.Background()

	now := time.Now().UTC()
	for _, event := range []domain.AuditEvent{
		{Ts: now.Add(-3 * time.Hour), Action: domain.AuditProjectCreate, TargetProjectID: "p1"},
		{Ts: now.Add(-2 * time.Hour), Action: domain.AuditProjectDelete, TargetProjectID: "p1"},
		{Ts: now.Add(-time.Hour), Action: domain.AuditProjectCreate, TargetProjectID: "p2"},
	} {
		if err := repo.Record(ctx, event); err != nil {
			t.Fatalf("Record: %v", err)
		}
	}

	byAction, err := usecase.List(ctx, domain.AuditQuery{Action: " project.create "})
	if err != nil {
		t.Fatalf("List by action: %v", err)
	}
	if len(byAction) != 2 {
		t.Fatalf("by action = %d events, want 2", len(byAction))
	}
	if byAction[0].TargetProjectID != "p2" {
		t.Fatalf("newest first: got %q, want p2", byAction[0].TargetProjectID)
	}

	byProject, err := usecase.List(ctx, domain.AuditQuery{ProjectID: "p1"})
	if err != nil {
		t.Fatalf("List by project: %v", err)
	}
	if len(byProject) != 2 {
		t.Fatalf("by project = %d events, want 2", len(byProject))
	}

	older, err := usecase.List(ctx, domain.AuditQuery{Before: now.Add(-90 * time.Minute)})
	if err != nil {
		t.Fatalf("List before cursor: %v", err)
	}
	if len(older) != 2 {
		t.Fatalf("before cursor = %d events, want 2", len(older))
	}
	for _, event := range older {
		if !event.Ts.Before(now.Add(-90 * time.Minute)) {
			t.Fatalf("cursor leaked newer event at %s", event.Ts)
		}
	}
}
