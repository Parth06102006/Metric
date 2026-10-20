package ingest_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"traffic-center/domain"
	"traffic-center/ingest"
	"traffic-center/metrics"
	metricsmemory "traffic-center/metrics/repository/memory"
	"traffic-center/project"
	projectmemory "traffic-center/project/repository/memory"
)

func setup(t *testing.T) (*ingest.Usecase, *metrics.Usecase, string, string) {
	t.Helper()

	projects := projectmemory.New()
	created, err := project.NewUsecase(projects).Create(context.Background(), "Test")
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	records := metricsmemory.New(60 * time.Second)
	return ingest.NewUsecase(projects, records), metrics.NewUsecase(records, nil, 60*time.Second), created.ID, created.APIKey
}

func record() domain.LogRecord {
	return domain.LogRecord{
		Ts: time.Now(), Endpoint: "/checkout", Method: "POST", Status: 200,
		LatencyMs: 25, IP: "203.0.113.7", PayloadBytes: 512,
	}
}

func TestIngestAccepted(t *testing.T) {
	usecase, metricsUsecase, projectID, apiKey := setup(t)

	accepted, err := usecase.Ingest(context.Background(), projectID, apiKey, []domain.LogRecord{record(), record()})
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if accepted != 2 {
		t.Fatalf("accepted = %d, want 2", accepted)
	}

	snapshot, err := metricsUsecase.Current(context.Background(), projectID)
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if snapshot.UniqueIPs != 1 || snapshot.RPS == 0 {
		t.Fatalf("unexpected snapshot: %+v", snapshot)
	}
}

func TestIngestRejectsBadCredentials(t *testing.T) {
	usecase, _, projectID, _ := setup(t)

	if _, err := usecase.Ingest(context.Background(), projectID, "wrong", []domain.LogRecord{record()}); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("error = %v, want unauthorized", err)
	}
}

func TestIngestRejectsUnknownProject(t *testing.T) {
	usecase, _, _, apiKey := setup(t)

	if _, err := usecase.Ingest(context.Background(), "missing", apiKey, []domain.LogRecord{record()}); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("error = %v, want unauthorized", err)
	}
}

func TestIngestValidatesBatch(t *testing.T) {
	usecase, _, projectID, apiKey := setup(t)

	if _, err := usecase.Ingest(context.Background(), projectID, apiKey, nil); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("empty batch error = %v, want validation", err)
	}

	bad := record()
	bad.Status = 42
	if _, err := usecase.Ingest(context.Background(), projectID, apiKey, []domain.LogRecord{bad}); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("invalid record error = %v, want validation", err)
	}
}
