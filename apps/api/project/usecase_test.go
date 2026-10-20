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
