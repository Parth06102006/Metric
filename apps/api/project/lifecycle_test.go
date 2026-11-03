package project_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"traffic-center/domain"
	"traffic-center/metrics/repository/memory"
	"traffic-center/project"
	rowmemory "traffic-center/project/repository/memory"
)

func timeNow() time.Time {
	return time.Now().UTC()
}

type recordingBroadcaster struct {
	dropped []string
}

func (b *recordingBroadcaster) BroadcastSnapshot(string, domain.Snapshot) {}
func (b *recordingBroadcaster) BroadcastAnomaly(string, domain.Anomaly)   {}
func (b *recordingBroadcaster) Drop(projectID string) {
	b.dropped = append(b.dropped, projectID)
}

func TestLifecycleRotateKey(t *testing.T) {
	repo := rowmemory.New()
	usecase := project.NewUsecase(repo)
	lifecycle := project.NewLifecycle(repo, memory.New(time.Minute), nil)

	created, err := usecase.Create(context.Background(), "Rotate Target")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	ok, err := repo.Authenticate(context.Background(), created.ID, created.APIKey)
	if err != nil || !ok {
		t.Fatalf("original key authenticate = %v, %v", ok, err)
	}

	rotated, err := lifecycle.RotateKey(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("RotateKey: %v", err)
	}
	if rotated.APIKey == created.APIKey {
		t.Fatal("rotated key must differ")
	}
	if rotated.Name != created.Name {
		t.Fatalf("rotated name = %q, want %q", rotated.Name, created.Name)
	}

	ok, err = repo.Authenticate(context.Background(), created.ID, created.APIKey)
	if err != nil || ok {
		t.Fatalf("old key authenticate after rotation = %v, %v, want false", ok, err)
	}
	ok, err = repo.Authenticate(context.Background(), created.ID, rotated.APIKey)
	if err != nil || !ok {
		t.Fatalf("new key authenticate after rotation = %v, %v", ok, err)
	}

	if _, err := lifecycle.RotateKey(context.Background(), "missing"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("RotateKey missing = %v, want ErrNotFound", err)
	}
	if _, err := lifecycle.RotateKey(context.Background(), ""); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("RotateKey empty id = %v, want validation", err)
	}
}

func TestLifecycleRevokeKey(t *testing.T) {
	repo := rowmemory.New()
	usecase := project.NewUsecase(repo)
	lifecycle := project.NewLifecycle(repo, nil, nil)

	created, err := usecase.Create(context.Background(), "Revoke Target")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := lifecycle.RevokeKey(context.Background(), created.ID); err != nil {
		t.Fatalf("RevokeKey: %v", err)
	}

	ok, err := repo.Authenticate(context.Background(), created.ID, created.APIKey)
	if err != nil || ok {
		t.Fatalf("authenticate with revoked key = %v, %v, want false", ok, err)
	}

	// Revocation is idempotent for an existing project.
	if err := lifecycle.RevokeKey(context.Background(), created.ID); err != nil {
		t.Fatalf("second RevokeKey = %v, want nil", err)
	}

	if err := lifecycle.RevokeKey(context.Background(), "missing"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("RevokeKey missing = %v, want ErrNotFound", err)
	}
}

func TestLifecycleDeleteProject(t *testing.T) {
	repo := rowmemory.New()
	usecase := project.NewUsecase(repo)
	records := memory.New(time.Minute)
	broadcaster := &recordingBroadcaster{}
	lifecycle := project.NewLifecycle(repo, records, broadcaster)

	ctx := context.Background()
	created, err := usecase.Create(ctx, "Doomed")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := records.Append(ctx, created.ID, []domain.LogRecord{{Ts: timeNow(), Endpoint: "/a", Method: "GET", Status: 200}}); err != nil {
		t.Fatalf("Append records: %v", err)
	}

	if err := lifecycle.DeleteProject(ctx, created.ID); err != nil {
		t.Fatalf("DeleteProject: %v", err)
	}

	if _, err := usecase.Get(ctx, created.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("Get after delete = %v, want ErrNotFound", err)
	}
	ok, err := repo.Authenticate(ctx, created.ID, created.APIKey)
	if err != nil || ok {
		t.Fatalf("authenticate after delete = %v, %v, want false", ok, err)
	}
	if len(records.ActiveProjects(ctx)) != 0 {
		t.Fatal("live record window must be purged on delete")
	}
	if len(broadcaster.dropped) != 1 || broadcaster.dropped[0] != created.ID {
		t.Fatalf("Drop calls = %v, want [%s]", broadcaster.dropped, created.ID)
	}

	if err := lifecycle.DeleteProject(ctx, created.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("second DeleteProject = %v, want ErrNotFound", err)
	}
	if len(broadcaster.dropped) != 1 {
		t.Fatalf("Drop must not run for missing projects: %v", broadcaster.dropped)
	}
}

func TestLifecycleAssignOwner(t *testing.T) {
	repo := rowmemory.New()
	usecase := project.NewUsecase(repo)
	broadcaster := &recordingBroadcaster{}
	lifecycle := project.NewLifecycle(repo, nil, broadcaster)
	ctx := context.Background()

	created, err := usecase.Create(ctx, "Handover")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Owner emails are normalized before storage.
	updated, err := lifecycle.AssignOwner(ctx, created.ID, "  Alice@Example.COM ")
	if err != nil {
		t.Fatalf("AssignOwner: %v", err)
	}
	if updated.OwnerEmail != "alice@example.com" {
		t.Fatalf("OwnerEmail = %q, want alice@example.com", updated.OwnerEmail)
	}
	if len(broadcaster.dropped) != 1 || broadcaster.dropped[0] != created.ID {
		t.Fatalf("owner change must disconnect streams: %v", broadcaster.dropped)
	}
	if _, err := lifecycle.AssignOwner(ctx, created.ID, "ALICE@example.com"); err != nil {
		t.Fatalf("reassign same owner: %v", err)
	}
	if len(broadcaster.dropped) != 1 {
		t.Fatalf("same normalized owner must not disconnect streams: %v", broadcaster.dropped)
	}

	aliceCtx := domain.WithIdentity(ctx, domain.Identity{Email: "alice@example.com"})
	if _, err := usecase.Get(aliceCtx, created.ID); err != nil {
		t.Fatalf("owner cannot read assigned project: %v", err)
	}

	bobCtx := domain.WithIdentity(ctx, domain.Identity{Email: "bob@example.com"})
	if _, err := usecase.Get(bobCtx, created.ID); !errors.Is(err, domain.ErrAccessDenied) {
		t.Fatalf("non-owner get = %v, want ErrAccessDenied", err)
	}

	// An empty owner makes the project admin-only again.
	if _, err := lifecycle.AssignOwner(ctx, created.ID, ""); err != nil {
		t.Fatalf("AssignOwner(empty): %v", err)
	}
	if len(broadcaster.dropped) != 2 {
		t.Fatalf("clearing ownership must disconnect streams: %v", broadcaster.dropped)
	}
	if _, err := usecase.Get(aliceCtx, created.ID); !errors.Is(err, domain.ErrAccessDenied) {
		t.Fatalf("get after unassign = %v, want ErrAccessDenied", err)
	}

	if _, err := lifecycle.AssignOwner(ctx, created.ID, "not-an-email"); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("AssignOwner(invalid) = %v, want validation", err)
	}
	if _, err := lifecycle.AssignOwner(ctx, "missing", "alice@example.com"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("AssignOwner(missing) = %v, want ErrNotFound", err)
	}
	if _, err := lifecycle.AssignOwner(ctx, "", "alice@example.com"); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("AssignOwner(no id) = %v, want validation", err)
	}
	if len(broadcaster.dropped) != 2 {
		t.Fatalf("failed assignments must not disconnect streams: %v", broadcaster.dropped)
	}
}
