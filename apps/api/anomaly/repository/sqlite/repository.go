package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
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

// SaveWithAnalysis commits an alert and its bounded queue entry together.
// The state intentionally excludes raw paths and IP addresses.
func (r *Repository) SaveWithAnalysis(ctx context.Context, projectID string, a *domain.Anomaly, snapshot domain.Snapshot, maxPending int) error {
	state := domain.IncidentEvidence{
		TriggerType: a.Type, Severity: a.Severity, Value: a.Value, Threshold: a.Threshold,
		WindowSeconds: snapshot.WindowS, RequestCount: snapshot.SampleCount,
		RPS: snapshot.RPS, P50: snapshot.P50, P95: snapshot.P95, P99: snapshot.P99,
		ErrorRate: snapshot.ErrorRate, TopIPRPS: snapshot.TopIPRPS, UniqueIPs: snapshot.UniqueIPs,
		Endpoints: snapshot.EndpointSummary,
	}
	if !snapshot.Ts.IsZero() {
		observed := snapshot.Ts.UTC()
		state.ObservedAt = &observed
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		return err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }() // Commit makes this a no-op.
	var pending int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM analysis_jobs WHERE status IN ('pending','processing')`).Scan(&pending); err != nil {
		return err
	}
	status := "pending"
	if maxPending <= 0 || pending >= maxPending {
		status = "skipped"
		a.Analysis += " Advisory analysis skipped: queue full."
	} else {
		a.Analysis += " Advisory analysis pending."
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO anomalies (project_id,type,severity,message,value,threshold,analysis,detected_at,analysis_status) VALUES (?,?,?,?,?,?,?,?,?)`, projectID, a.Type, a.Severity, a.Message, a.Value, a.Threshold, a.Analysis, a.DetectedAt.UnixMilli(), status)
	if err != nil {
		return err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return err
	}
	if status == "pending" {
		if _, err := tx.ExecContext(ctx, `INSERT INTO analysis_jobs(anomaly_id,project_id,state_json,status,created_at) VALUES(?,?,?,'pending',?)`, id, projectID, string(encoded), time.Now().UnixMilli()); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	a.ID = id
	a.AnalysisStatus = status
	return nil
}

func (r *Repository) Save(ctx context.Context, projectID string, anomaly *domain.Anomaly) error {
	result, err := r.db.ExecContext(ctx,
		`INSERT INTO anomalies
			(project_id, type, severity, message, value, threshold, analysis, detected_at, analysis_status)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		projectID, anomaly.Type, anomaly.Severity, anomaly.Message, anomaly.Value,
		anomaly.Threshold, anomaly.Analysis, anomaly.DetectedAt.UnixMilli(), anomaly.AnalysisStatus,
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
		`SELECT id, type, severity, message, value, threshold, analysis, detected_at, analysis_status, diagnosis_json
		 FROM anomalies
		 WHERE project_id = ?
		 ORDER BY detected_at DESC
		 LIMIT ?`,
		projectID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	anomalies := make([]domain.Anomaly, 0)
	for rows.Next() {
		var (
			anomaly    domain.Anomaly
			detectedAt int64
			diagnosis  string
		)
		if err := rows.Scan(
			&anomaly.ID, &anomaly.Type, &anomaly.Severity, &anomaly.Message,
			&anomaly.Value, &anomaly.Threshold, &anomaly.Analysis, &detectedAt, &anomaly.AnalysisStatus, &diagnosis,
		); err != nil {
			return nil, err
		}
		anomaly.DetectedAt = time.UnixMilli(detectedAt).UTC()
		if diagnosis != "" {
			var parsed domain.Diagnosis
			if err := json.Unmarshal([]byte(diagnosis), &parsed); err != nil {
				return nil, err
			}
			anomaly.Diagnosis = &parsed
		}
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
