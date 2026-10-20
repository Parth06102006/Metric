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

func setup(t *testing.T) (*threshold.Usecase, string) {
	t.Helper()

	projects := projectmemory.New()
	created, err := project.NewUsecase(projects).Create(context.Background(), "Test")
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	return threshold.NewUsecase(thresholdmemory.New(), projects), created.ID
}

func TestGetFallsBackToDefaults(t *testing.T) {
	usecase, projectID := setup(t)

	got, err := usecase.Get(context.Background(), projectID)
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
	if err := usecase.Update(context.Background(), projectID, want); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got, err := usecase.Get(context.Background(), projectID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != want {
		t.Fatalf("Get = %+v, want %+v", got, want)
	}
}

func TestUpdateValidation(t *testing.T) {
	usecase, projectID := setup(t)

	err := usecase.Update(context.Background(), projectID, domain.Threshold{ErrorRate: 101})
	if !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("Update error = %v, want validation", err)
	}
}

func TestUnknownProject(t *testing.T) {
	usecase, _ := setup(t)

	if _, err := usecase.Get(context.Background(), "missing"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("Get missing = %v, want not found", err)
	}
}
