package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
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

// List returns matching audit events, newest first. An empty query returns the
// most recent events up to the default cap; the usecase validates the limit.
func (r *Repository) List(ctx context.Context, query domain.AuditQuery) ([]domain.AuditEvent, error) {
	limit := query.Limit
	if limit <= 0 {
		limit = 100
	}

	conditions := make([]string, 0, 3)
	args := make([]any, 0, 4)

	if action := strings.TrimSpace(query.Action); action != "" {
		conditions = append(conditions, "action = ?")
		args = append(args, action)
	}
	if projectID := strings.TrimSpace(query.ProjectID); projectID != "" {
		conditions = append(conditions, "target_project_id = ?")
		args = append(args, projectID)
	}
	if !query.Before.IsZero() {
		ts := query.Before.UTC().UnixMilli()
		conditions = append(conditions, "(ts < ? OR (ts = ? AND id < ?))")
		args = append(args, ts, ts, query.BeforeID)
	}

	statement := `SELECT id, ts, actor_email, action, target_project_id, outcome, request_id, details_json
		 FROM audit_events`
	if len(conditions) > 0 {
		statement += " WHERE " + strings.Join(conditions, " AND ")
	}
	statement += " ORDER BY ts DESC, id DESC LIMIT ?"
	args = append(args, limit)

	rows, err := r.db.QueryContext(ctx, statement, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

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
