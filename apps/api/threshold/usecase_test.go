package threshold_test

import (
	"context"
	"errors"
	"testing"

	"traffic-center/domain"
	"traffic-center/project"
	projectmemory "traffic-center/project/repository/memory"
	"traffic-center/threshold"
	thresholdmemory "traffic-center/threshold/repository/memory"
)

func adminContext() context.Context {
	return domain.WithIdentity(context.Background(), domain.Identity{Email: "admin@example.com", Admin: true})
}

func setup(t *testing.T) (*threshold.Usecase, string) {
	t.Helper()

	projects := projectmemory.New()
	created, err := project.NewUsecase(projects).Create(adminContext(), "Test")
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	return threshold.NewUsecase(thresholdmemory.New(), projects), created.ID
}

func TestThresholdAuthorizationFailsClosed(t *testing.T) {
	usecase, id := setup(t)
	for _, ctx := range []context.Context{context.Background(), domain.WithIdentity(context.Background(), domain.Identity{Admin: true})} {
		if _, err := usecase.Get(ctx, id); !errors.Is(err, domain.ErrUnauthorized) {
			t.Fatalf("get without identity: %v", err)
		}
		if err := usecase.Update(ctx, id, domain.DefaultThreshold()); !errors.Is(err, domain.ErrUnauthorized) {
			t.Fatalf("update without identity: %v", err)
		}
	}
	owner := domain.WithIdentity(context.Background(), domain.Identity{Email: "admin@example.com"})
	if _, err := usecase.Get(owner, id); err != nil {
		t.Fatalf("owner get: %v", err)
	}
	if err := usecase.Update(owner, id, domain.DefaultThreshold()); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("owner update: %v", err)
	}
	other := domain.WithIdentity(context.Background(), domain.Identity{Email: "other@example.com"})
	if _, err := usecase.Get(other, id); !errors.Is(err, domain.ErrAccessDenied) {
		t.Fatalf("other get: %v", err)
	}
}

func TestGetFallsBackToDefaults(t *testing.T) {
	usecase, projectID := setup(t)

	got, err := usecase.Get(adminContext(), projectID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != domain.DefaultThreshold() {
		t.Fatalf("Get = %+v, want defaults", got)
	}
}

func TestUpdateThenGet(t *testing.T) {
	usecase, projectID := setup(t)

	want := domain.Threshold{P95Ms: 250, ErrorRate: 2, RPSMax: 80, ReqPerIPMax: 10}
	if err := usecase.Update(adminContext(), projectID, want); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got, err := usecase.Get(adminContext(), projectID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != want {
		t.Fatalf("Get = %+v, want %+v", got, want)
	}
}

func TestUpdateValidation(t *testing.T) {
	usecase, projectID := setup(t)

	err := usecase.Update(adminContext(), projectID, domain.Threshold{ErrorRate: 101})
	if !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("Update error = %v, want validation", err)
	}
}

func TestUnknownProject(t *testing.T) {
	usecase, _ := setup(t)

	if _, err := usecase.Get(adminContext(), "missing"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("Get missing = %v, want not found", err)
	}
}
