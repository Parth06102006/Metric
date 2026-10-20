package sqlite

import (
	"context"
	"database/sql"
	"time"

	"traffic-center/domain"
)

// Repository is the SQLite adapter for anomalies.
type Repository struct {
	db *sql.DB
}

// New creates an anomaly repository.
func New(db *sql.DB) *Repository {
	return &Repository{db: db}
}

func (r *Repository) Save(ctx context.Context, projectID string, anomaly *domain.Anomaly) error {
	result, err := r.db.ExecContext(ctx,
		`INSERT INTO anomalies
			(project_id, type, severity, message, value, threshold, analysis, detected_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		projectID, anomaly.Type, anomaly.Severity, anomaly.Message, anomaly.Value,
		anomaly.Threshold, anomaly.Analysis, anomaly.DetectedAt.UnixMilli(),
	)
	if err != nil {
		return err
	}

	id, err := result.LastInsertId()
	if err == nil {
		anomaly.ID = id
	}
	return nil
}

func (r *Repository) List(ctx context.Context, projectID string, limit int) ([]domain.Anomaly, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}

	rows, err := r.db.QueryContext(ctx,
		`SELECT id, type, severity, message, value, threshold, analysis, detected_at
		 FROM anomalies
		 WHERE project_id = ?
		 ORDER BY detected_at DESC
		 LIMIT ?`,
		projectID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	anomalies := make([]domain.Anomaly, 0)
	for rows.Next() {
		var (
			anomaly    domain.Anomaly
			detectedAt int64
		)
		if err := rows.Scan(
			&anomaly.ID, &anomaly.Type, &anomaly.Severity, &anomaly.Message,
			&anomaly.Value, &anomaly.Threshold, &anomaly.Analysis, &detectedAt,
		); err != nil {
			return nil, err
		}
		anomaly.DetectedAt = time.UnixMilli(detectedAt).UTC()
		anomalies = append(anomalies, anomaly)
	}
	return anomalies, rows.Err()
}

func (r *Repository) HasRecent(ctx context.Context, projectID, anomalyType string, since time.Time) (bool, error) {
	var count int
	err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(1) FROM anomalies
		 WHERE project_id = ? AND type = ? AND detected_at >= ?`,
		projectID, anomalyType, since.UnixMilli(),
	).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}
