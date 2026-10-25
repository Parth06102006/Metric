package project_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"traffic-center/domain"
	"traffic-center/project"
	"traffic-center/project/repository/memory"
)

func TestCreateAndAuthenticate(t *testing.T) {
	repo := memory.New()
	usecase := project.NewUsecase(repo)

	created, err := usecase.Create(context.Background(), "  Checkout API  ")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.Name != "Checkout API" {
		t.Fatalf("Name = %q, want trimmed name", created.Name)
	}
	if created.ID == "" || created.APIKey == "" {
		t.Fatal("expected ID and API key")
	}

	ok, err := repo.Authenticate(context.Background(), created.ID, created.APIKey)
	if err != nil || !ok {
		t.Fatalf("Authenticate with correct key = %v, %v", ok, err)
	}

	ok, err = repo.Authenticate(context.Background(), created.ID, "wrong")
	if err != nil || ok {
		t.Fatalf("Authenticate with wrong key = %v, %v", ok, err)
	}

	got, err := usecase.Get(context.Background(), created.ID)
	if err != nil || got.Name != created.Name {
		t.Fatalf("Get = %+v, %v", got, err)
	}
}

func TestCreateValidation(t *testing.T) {
	usecase := project.NewUsecase(memory.New())

	if _, err := usecase.Create(context.Background(), "   "); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("empty name error = %v, want validation", err)
	}

	if _, err := usecase.Create(context.Background(), strings.Repeat("x", 101)); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("long name error = %v, want validation", err)
	}
}

func TestGetMissing(t *testing.T) {
	usecase := project.NewUsecase(memory.New())
	if _, err := usecase.Get(context.Background(), "missing"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("Get missing = %v, want not found", err)
	}
}

func TestProjectOwnershipAndAccessControl(t *testing.T) {
	repo := memory.New()
	usecase := project.NewUsecase(repo)

	userA := domain.Identity{Email: "alice@example.com", User: "alice", Admin: false}
	userB := domain.Identity{Email: "bob@example.com", User: "bob", Admin: false}
	admin := domain.Identity{Email: "admin@example.com", User: "admin", Admin: true}

	ctxUserA := domain.WithIdentity(context.Background(), userA)
	ctxUserB := domain.WithIdentity(context.Background(), userB)
	ctxAdmin := domain.WithIdentity(context.Background(), admin)

	// User A creates Project A
	createdA, err := usecase.Create(ctxUserA, "Alice Project")
	if err != nil {
		t.Fatalf("create Alice project: %v", err)
	}
	if createdA.OwnerEmail != "alice@example.com" {
		t.Fatalf("OwnerEmail = %q, want alice@example.com", createdA.OwnerEmail)
	}

	// User B creates Project B
	createdB, err := usecase.Create(ctxUserB, "Bob Project")
	if err != nil {
		t.Fatalf("create Bob project: %v", err)
	}

	// Owner (User A) can get Project A
	got, err := usecase.Get(ctxUserA, createdA.ID)
	if err != nil || got.ID != createdA.ID {
		t.Fatalf("User A get Project A error = %v", err)
	}

	// User B CANNOT get Project A (must return ErrNotFound to avoid leaking existence)
	_, err = usecase.Get(ctxUserB, createdA.ID)
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("User B get Project A error = %v, want ErrNotFound", err)
	}

	// Admin CAN get Project A
	got, err = usecase.Get(ctxAdmin, createdA.ID)
	if err != nil || got.ID != createdA.ID {
		t.Fatalf("Admin get Project A error = %v", err)
	}

	// Listing: User A sees only Project A
	listA, err := usecase.List(ctxUserA)
	if err != nil {
		t.Fatalf("User A list error = %v", err)
	}
	if len(listA) != 1 || listA[0].ID != createdA.ID {
		t.Fatalf("User A list = %+v, want only Project A", listA)
	}

	// Listing: User B sees only Project B
	listB, err := usecase.List(ctxUserB)
	if err != nil {
		t.Fatalf("User B list error = %v", err)
	}
	if len(listB) != 1 || listB[0].ID != createdB.ID {
		t.Fatalf("User B list = %+v, want only Project B", listB)
	}

	// Listing: Admin sees both Project A and Project B
	listAdmin, err := usecase.List(ctxAdmin)
	if err != nil {
		t.Fatalf("Admin list error = %v", err)
	}
	if len(listAdmin) != 2 {
		t.Fatalf("Admin list count = %d, want 2", len(listAdmin))
	}
}

func TestLegacyProjectAccess(t *testing.T) {
	repo := memory.New()
	usecase := project.NewUsecase(repo)

	// Simulate a legacy project with no owner
	legacy := domain.Project{ID: "legacy1", Name: "Legacy Project", OwnerEmail: ""}
	if err := repo.Create(context.Background(), legacy, "hash"); err != nil {
		t.Fatalf("create legacy project: %v", err)
	}

	user := domain.Identity{Email: "user@example.com", User: "user", Admin: false}
	admin := domain.Identity{Email: "admin@example.com", User: "admin", Admin: true}

	ctxUser := domain.WithIdentity(context.Background(), user)
	ctxAdmin := domain.WithIdentity(context.Background(), admin)

	// Regular user cannot get legacy project (returns ErrNotFound)
	_, err := usecase.Get(ctxUser, "legacy1")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("regular user get legacy error = %v, want ErrNotFound", err)
	}

	// Regular user does not see legacy project in list
	userList, err := usecase.List(ctxUser)
	if err != nil {
		t.Fatalf("regular user list error: %v", err)
	}
	if len(userList) != 0 {
		t.Fatalf("regular user list = %+v, want empty", userList)
	}

	// Admin can get legacy project
	got, err := usecase.Get(ctxAdmin, "legacy1")
	if err != nil || got.ID != "legacy1" {
		t.Fatalf("admin get legacy error = %v", err)
	}

	// Admin sees legacy project in list
	adminList, err := usecase.List(ctxAdmin)
	if err != nil {
		t.Fatalf("admin list error: %v", err)
	}
	if len(adminList) != 1 || adminList[0].ID != "legacy1" {
		t.Fatalf("admin list = %+v, want legacy1", adminList)
	}
}
