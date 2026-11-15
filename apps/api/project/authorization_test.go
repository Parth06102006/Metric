package project_test

import (
	"context"
	"errors"
	"testing"

	"traffic-center/domain"
	"traffic-center/internal/projectgate"
	"traffic-center/project"
	"traffic-center/project/repository/memory"
)

func TestControlPlaneUsecasesRequireIdentityAndAdmin(t *testing.T) {
	repo := memory.New()
	use := project.NewUsecase(repo)
	life := project.NewLifecycle(repo, nil, nil, projectgate.New())
	created, err := use.Create(adminContext(), "secured")
	if err != nil {
		t.Fatal(err)
	}
	calls := map[string]func(context.Context) error{
		"create":      func(ctx context.Context) error { _, e := use.Create(ctx, "unexpected"); return e },
		"known-users": func(ctx context.Context) error { _, e := use.KnownUsers(ctx); return e },
		"viewers":     func(ctx context.Context) error { _, e := use.Viewers(ctx, created.ID); return e },
		"rotate":      func(ctx context.Context) error { _, e := life.RotateKey(ctx, created.ID); return e },
		"revoke":      func(ctx context.Context) error { return life.RevokeKey(ctx, created.ID) },
		"owner": func(ctx context.Context) error {
			_, e := life.AssignOwner(ctx, created.ID, "owner@example.com")
			return e
		},
		"add-viewer":    func(ctx context.Context) error { return life.AddViewer(ctx, created.ID, "viewer@example.com") },
		"remove-viewer": func(ctx context.Context) error { return life.RemoveViewer(ctx, created.ID, "viewer@example.com") },
		"delete":        func(ctx context.Context) error { return life.DeleteProject(ctx, created.ID) },
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			for _, ctx := range []context.Context{context.Background(), domain.WithIdentity(context.Background(), domain.Identity{Admin: true})} {
				if err := call(ctx); !errors.Is(err, domain.ErrUnauthorized) {
					t.Fatalf("missing identity: %v", err)
				}
			}
			owner := domain.WithIdentity(context.Background(), domain.Identity{Email: "admin@example.com"})
			if err := call(owner); !errors.Is(err, domain.ErrForbidden) {
				t.Fatalf("nonadmin: %v", err)
			}
		})
	}
	for _, ctx := range []context.Context{context.Background(), domain.WithIdentity(context.Background(), domain.Identity{Admin: true})} {
		if _, err := use.Get(ctx, created.ID); !errors.Is(err, domain.ErrUnauthorized) {
			t.Fatalf("get missing identity: %v", err)
		}
		if _, err := use.List(ctx); !errors.Is(err, domain.ErrUnauthorized) {
			t.Fatalf("list missing identity: %v", err)
		}
	}
	if ok, err := repo.Authenticate(context.Background(), created.ID, created.APIKey); err != nil || !ok {
		t.Fatalf("denied operations mutated credentials: %v", err)
	}
	if _, err := use.Get(adminContext(), created.ID); err != nil {
		t.Fatal(err)
	}
}
