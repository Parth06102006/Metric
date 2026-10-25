package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"traffic-center/domain"
)

// Repository is the SQLite adapter for audit events. Records are append-only:
// there is no update or delete path.
type Repository struct {
	db *sql.DB
}

// New creates an audit repository.
func New(db *sql.DB) *Repository {
	return &Repository{db: db}
}

func (r *Repository) Record(ctx context.Context, event domain.AuditEvent) error {
	details, err := json.Marshal(domain.RedactDetails(event.Details))
	if err != nil {
		return err
	}

	ts := event.Ts
	if ts.IsZero() {
		ts = time.Now().UTC()
	}

	_, err = r.db.ExecContext(ctx,
		`INSERT INTO audit_events
			(ts, actor_email, action, target_project_id, outcome, request_id, details_json)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		ts.UTC().UnixMilli(), event.ActorEmail, event.Action, event.TargetProjectID,
		event.Outcome, event.RequestID, string(details),
	)
	return err
}

// Prune deletes audit events older than the cutoff. This is the single
// operational exception to append-only storage: retention runs out-of-band,
// and no HTTP endpoint can invoke it.
func (r *Repository) Prune(ctx context.Context, before time.Time) (int64, error) {
	result, err := r.db.ExecContext(ctx, `DELETE FROM audit_events WHERE ts < ?`, before.UTC().UnixMilli())
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (r *Repository) List(ctx context.Context, limit int) ([]domain.AuditEvent, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, ts, actor_email, action, target_project_id, outcome, request_id, details_json
		 FROM audit_events ORDER BY ts DESC, id DESC LIMIT ?`, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	events := make([]domain.AuditEvent, 0)
	for rows.Next() {
		var (
			event       domain.AuditEvent
			tsMillis    int64
			detailsJSON string
		)
		if err := rows.Scan(&event.ID, &tsMillis, &event.ActorEmail, &event.Action,
			&event.TargetProjectID, &event.Outcome, &event.RequestID, &detailsJSON); err != nil {
			return nil, err
		}
		event.Ts = time.UnixMilli(tsMillis).UTC()
		event.Details = map[string]string{}
		if detailsJSON != "" {
			_ = json.Unmarshal([]byte(detailsJSON), &event.Details)
		}
		events = append(events, event)
	}
	return events, rows.Err()
}
