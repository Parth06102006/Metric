package sqlite

import (
	"context"
	"database/sql"
	"errors"

	"traffic-center/domain"
)

// Repository is the SQLite adapter for thresholds.
type Repository struct {
	db *sql.DB
}

// New creates a threshold repository.
func New(db *sql.DB) *Repository {
	return &Repository{db: db}
}

func (r *Repository) Get(ctx context.Context, projectID string) (domain.Threshold, error) {
	var thresholds domain.Threshold
	err := r.db.QueryRowContext(ctx,
		`SELECT p95_ms, error_rate, rps_max, req_per_ip_max FROM thresholds WHERE project_id = ?`,
		projectID,
	).Scan(&thresholds.P95Ms, &thresholds.ErrorRate, &thresholds.RPSMax, &thresholds.ReqPerIPMax)

	if errors.Is(err, sql.ErrNoRows) {
		return domain.Threshold{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.Threshold{}, err
	}
	return thresholds, nil
}

func (r *Repository) Upsert(ctx context.Context, projectID string, thresholds domain.Threshold) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO thresholds (project_id, p95_ms, error_rate, rps_max, req_per_ip_max)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(project_id) DO UPDATE SET
			p95_ms = excluded.p95_ms,
			error_rate = excluded.error_rate,
			rps_max = excluded.rps_max,
			req_per_ip_max = excluded.req_per_ip_max`,
		projectID, thresholds.P95Ms, thresholds.ErrorRate, thresholds.RPSMax, thresholds.ReqPerIPMax,
	)
	return err
}
