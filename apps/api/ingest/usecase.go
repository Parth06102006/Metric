package ingest

import (
	"context"

	"traffic-center/domain"
)

// MaxBatchSize is the maximum number of log records accepted per request.
const MaxBatchSize = 1000

// Usecase authenticates ingest clients and appends records to the live window.
type Usecase struct {
	projects domain.ProjectRepository
	records  domain.RecordRepository
}

// NewUsecase wires the ingest usecase.
func NewUsecase(projects domain.ProjectRepository, records domain.RecordRepository) *Usecase {
	return &Usecase{projects: projects, records: records}
}

// Ingest validates the API key and the batch, then stores the records.
// It returns domain.ErrUnauthorized for any credential problem, without
// revealing whether the project exists.
func (u *Usecase) Ingest(ctx context.Context, projectID, apiKey string, records []domain.LogRecord) (int, error) {
	if projectID == "" {
		return 0, domain.Validationf("project id is required")
	}
	if apiKey == "" {
		return 0, domain.ErrUnauthorized
	}

	ok, err := u.projects.Authenticate(ctx, projectID, apiKey)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, domain.ErrUnauthorized
	}

	if len(records) == 0 || len(records) > MaxBatchSize {
		return 0, domain.Validationf("logs must contain between 1 and %d records", MaxBatchSize)
	}

	for _, record := range records {
		if err := record.Validate(); err != nil {
			return 0, err
		}
	}

	if err := u.records.Append(ctx, projectID, records); err != nil {
		return 0, err
	}

	return len(records), nil
}
