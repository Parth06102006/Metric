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
	"traffic-center/domain"
)

type fakeRepo struct {
	recorded []domain.AuditEvent
	recordLimit int
	listLimit   int
	recordErr   error
}

func (r *fakeRepo) Record(_ context.Context, event domain.AuditEvent) error {
	if r.recordErr != nil {
		return r.recordErr
	}
	r.recorded = append(r.recorded, event)
	return nil
}

func (r *fakeRepo) List(_ context.Context, limit int) ([]domain.AuditEvent, error) {
	r.listLimit = limit
	return r.recorded, nil
}

func (r *fakeRepo) Prune(_ context.Context, before time.Time) (int64, error) {
	kept := r.recorded[:0]
	var pruned int64
	for _, event := range r.recorded {
		if event.Ts.Before(before) {
			pruned++
			continue
		}
		kept = append(kept, event)
	}
	r.recorded = kept
	return pruned, nil
}

func TestLogStoresRedactedEvent(t *testing.T) {
	repo := &fakeRepo{}
	usecase := audit.NewUsecase(repo, nil)

	usecase.Log(context.Background(), "Admin@Example.COM ", domain.AuditProjectCreate, "p1",
		domain.AuditOutcomeSuccess, map[string]string{"project_name": "Checkout", "api_key": "sk-should-not-be-stored"})

	if len(repo.recorded) != 1 {
		t.Fatalf("recorded events = %d, want 1", len(repo.recorded))
	}
	event := repo.recorded[0]
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
	repo := &fakeRepo{recordErr: errors.New("disk on fire")}
	usecase := audit.NewUsecase(repo, slog.New(slog.NewTextHandler(&buf, nil)))

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
	repo := &fakeRepo{}
	usecase := audit.NewUsecase(repo, nil)

	if _, err := usecase.List(context.Background(), 0); err != nil {
		t.Fatalf("List(0): %v", err)
	}
	if repo.listLimit != 100 {
		t.Fatalf("default limit = %d, want 100", repo.listLimit)
	}

	if _, err := usecase.List(context.Background(), audit.MaxListSize+1); err != nil {
		t.Fatalf("List(over max): %v", err)
	}
	if repo.listLimit != 100 {
		t.Fatalf("oversized limit = %d, want clamped to 100", repo.listLimit)
	}

	if _, err := usecase.List(context.Background(), 25); err != nil {
		t.Fatalf("List(25): %v", err)
	}
	if repo.listLimit != 25 {
		t.Fatalf("explicit limit = %d, want 25", repo.listLimit)
	}
}
