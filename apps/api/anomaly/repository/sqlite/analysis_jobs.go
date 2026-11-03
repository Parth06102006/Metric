package sqlite

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"traffic-center/domain"
)

// AnalysisJob is a leased durable job; LeaseToken prevents stale workers committing.
type AnalysisJob struct {
	AnomalyID  int64
	ProjectID  string
	StateJSON  string
	Attempts   int
	LeaseToken string
	CreatedAt  time.Time
}

func (r *Repository) ClaimAnalysis(ctx context.Context, now time.Time, lease time.Duration) (AnalysisJob, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return AnalysisJob{}, err
	}
	defer func() { _ = tx.Rollback() }() // Commit makes this a no-op.
	var job AnalysisJob
	var created int64
	err = tx.QueryRowContext(ctx, `SELECT anomaly_id,project_id,state_json,attempts,created_at FROM analysis_jobs WHERE (status='pending' AND next_attempt<=?) OR (status='processing' AND lease_until<=?) ORDER BY created_at,anomaly_id LIMIT 1`, now.UnixMilli(), now.UnixMilli()).Scan(&job.AnomalyID, &job.ProjectID, &job.StateJSON, &job.Attempts, &created)
	if err != nil {
		return AnalysisJob{}, err
	}
	job.CreatedAt = time.UnixMilli(created).UTC()
	b := make([]byte, 16)
	if _, err = rand.Read(b); err != nil {
		return AnalysisJob{}, err
	}
	job.LeaseToken = hex.EncodeToString(b)
	job.Attempts++
	_, err = tx.ExecContext(ctx, `UPDATE analysis_jobs SET status='processing',attempts=?,lease_until=?,lease_token=? WHERE anomaly_id=?`, job.Attempts, now.Add(lease).UnixMilli(), job.LeaseToken, job.AnomalyID)
	if err != nil {
		return AnalysisJob{}, err
	}
	if err = tx.Commit(); err != nil {
		return AnalysisJob{}, err
	}
	return job, nil
}

func (r *Repository) CompleteAnalysis(ctx context.Context, job AnalysisJob, diagnosis domain.Diagnosis, narrative string) error {
	payload, err := json.Marshal(diagnosis)
	if err != nil {
		return err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }() // Commit makes this a no-op.
	res, err := tx.ExecContext(ctx, `DELETE FROM analysis_jobs WHERE anomaly_id=? AND lease_token=? AND status='processing'`, job.AnomalyID, job.LeaseToken)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("analysis lease expired or job deleted")
	}
	res, err = tx.ExecContext(ctx, `UPDATE anomalies SET diagnosis_json=?,analysis=?,analysis_status='completed' WHERE id=? AND project_id=?`, string(payload), narrative, job.AnomalyID, job.ProjectID)
	if err != nil {
		return err
	}
	n, err = res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return sql.ErrNoRows
	}
	return tx.Commit()
}

func (r *Repository) FailAnalysis(ctx context.Context, job AnalysisJob, retryAt time.Time, permanent bool) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }() // Commit makes this a no-op.
	var res sql.Result
	if permanent {
		res, err = tx.ExecContext(ctx, `DELETE FROM analysis_jobs WHERE anomaly_id=? AND lease_token=? AND status='processing'`, job.AnomalyID, job.LeaseToken)
	} else {
		res, err = tx.ExecContext(ctx, `UPDATE analysis_jobs SET status='pending',next_attempt=?,lease_until=0,lease_token='' WHERE anomaly_id=? AND lease_token=? AND status='processing'`, retryAt.UnixMilli(), job.AnomalyID, job.LeaseToken)
	}
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("analysis lease expired or job deleted")
	}
	if permanent {
		if _, err = tx.ExecContext(ctx, `UPDATE anomalies SET analysis_status='failed', analysis=message || ' Advisory analysis unavailable.' WHERE id=? AND project_id=?`, job.AnomalyID, job.ProjectID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ReleaseAnalysis releases a job that never contacted the provider.
func (r *Repository) ReleaseAnalysis(ctx context.Context, job AnalysisJob) error {
	result, err := r.db.ExecContext(ctx, `UPDATE analysis_jobs SET status='pending',next_attempt=0,lease_until=0,lease_token='',attempts=MAX(attempts-1,0) WHERE anomaly_id=? AND lease_token=? AND status='processing'`, job.AnomalyID, job.LeaseToken)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("analysis lease expired or job deleted")
	}
	return nil
}

func (r *Repository) GetAnalysisAnomaly(ctx context.Context, projectID string, id int64) (domain.Anomaly, error) {
	var a domain.Anomaly
	var detected int64
	var diagnosis string
	err := r.db.QueryRowContext(ctx, `SELECT id,type,severity,message,value,threshold,analysis,detected_at,analysis_status,diagnosis_json FROM anomalies WHERE project_id=? AND id=?`, projectID, id).Scan(&a.ID, &a.Type, &a.Severity, &a.Message, &a.Value, &a.Threshold, &a.Analysis, &detected, &a.AnalysisStatus, &diagnosis)
	if err != nil {
		return a, err
	}
	a.DetectedAt = time.UnixMilli(detected).UTC()
	if diagnosis != "" {
		var d domain.Diagnosis
		if err = json.Unmarshal([]byte(diagnosis), &d); err != nil {
			return a, err
		}
		a.Diagnosis = &d
	}
	return a, nil
}
