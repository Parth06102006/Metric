package ingest

import (
	"context"
	"time"

	"traffic-center/domain"
)

// MaxBatchSize is the maximum number of log records accepted per request.
const MaxBatchSize = 1000

// Usecase authenticates ingest clients and appends records to the live window.
type Usecase struct {
	projects domain.ProjectRepository
	records  domain.RecordRepository
	gate     domain.ProjectGate
	window   time.Duration
}

// NewUsecase wires the ingest usecase.
func NewUsecase(projects domain.ProjectRepository, records domain.RecordRepository, gate domain.ProjectGate, windows ...time.Duration) *Usecase {
	window := time.Minute
	if len(windows) > 0 && windows[0] > 0 {
		window = windows[0]
	}
	return &Usecase{projects: projects, records: records, gate: gate, window: window}
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

	unlock, err := u.gate.Lock(ctx, projectID)
	if err != nil {
		return 0, err
	}
	defer unlock()

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

	now := time.Now()
	batch := make([]domain.LogRecord, len(records))
	for i, record := range records {
		if err := record.Validate(); err != nil {
			return 0, err
		}
		if record.Ts.Before(now.Add(-u.window)) || record.Ts.After(now.Add(5*time.Second)) {
			return 0, domain.Validationf("log record timestamp must be within the live window and at most 5 seconds ahead")
		}
		// Tolerate small sender clock skew without counting future events.
		if record.Ts.After(now) {
			record.Ts = now
		}
		batch[i] = record
	}

	if err := u.records.Append(ctx, projectID, batch); err != nil {
		return 0, err
	}

	return len(records), nil
}
