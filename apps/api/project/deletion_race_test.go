package project_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"traffic-center/domain"
	"traffic-center/ingest"
	"traffic-center/internal/projectgate"
	memory "traffic-center/metrics/repository/memory"
	"traffic-center/project"
	rowmemory "traffic-center/project/repository/memory"
)

type pausedAuthentication struct {
	domain.ProjectRepository
	started chan struct{}
	resume  chan struct{}
	once    sync.Once
}

func (p *pausedAuthentication) Authenticate(ctx context.Context, id, key string) (bool, error) {
	ok, err := p.ProjectRepository.Authenticate(ctx, id, key)
	p.once.Do(func() { close(p.started); <-p.resume })
	return ok, err
}

func TestDeletionWaitsForAuthenticatedIngestThenPurges(t *testing.T) {
	ctx := adminContext()
	base := rowmemory.New()
	created, err := project.NewUsecase(base).Create(ctx, "race")
	if err != nil {
		t.Fatal(err)
	}
	projects := &pausedAuthentication{ProjectRepository: base, started: make(chan struct{}), resume: make(chan struct{})}
	records := memory.New(time.Minute)
	gate := projectgate.New()
	ingestion := ingest.NewUsecase(projects, records, gate)
	lifecycle := project.NewLifecycle(base, records, nil, gate)
	batch := []domain.LogRecord{{Ts: time.Now(), Endpoint: "/x", Method: "GET", Status: 200, IP: "127.0.0.1"}}
	ingested := make(chan error, 1)
	go func() { _, err := ingestion.Ingest(ctx, created.ID, created.APIKey, batch); ingested <- err }()
	<-projects.started
	// A deletion with a deadline must wait on the in-flight ingest, not commit
	// while authentication has succeeded but append has not run.
	wait, cancel := context.WithTimeout(ctx, 30*time.Millisecond)
	defer cancel()
	if err := lifecycle.DeleteProject(wait, created.ID); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("delete while ingest blocked: %v", err)
	}
	close(projects.resume)
	if err := <-ingested; err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.DeleteProject(ctx, created.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := ingestion.Ingest(ctx, created.ID, created.APIKey, batch); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("post-delete ingest: %v", err)
	}
	if len(records.ActiveProjects(ctx)) != 0 {
		t.Fatal("deleted live window reappeared")
	}
}
