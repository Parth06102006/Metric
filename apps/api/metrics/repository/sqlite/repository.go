package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"traffic-center/domain"
)

// Repository is the SQLite adapter for computed snapshots.
type Repository struct {
	db *sql.DB
}

// New creates a snapshot repository.
func New(db *sql.DB) *Repository {
	return &Repository{db: db}
}

func (r *Repository) Save(ctx context.Context, projectID string, snapshot domain.Snapshot) error {
	endpoints, err := json.Marshal(snapshot.Endpoints)
	if err != nil {
		return err
	}

	_, err = r.db.ExecContext(ctx,
		`INSERT INTO metric_snapshots
			(project_id, ts, window_s, rps, p50, p95, p99, error_rate, unique_ips, top_ip_rps, avg_payload_bytes, endpoints_json)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		projectID, snapshot.Ts.UnixMilli(), snapshot.WindowS, snapshot.RPS, snapshot.P50, snapshot.P95,
		snapshot.P99, snapshot.ErrorRate, snapshot.UniqueIPs, snapshot.TopIPRPS,
		snapshot.AvgPayloadBytes, string(endpoints),
	)
	return err
}

func (r *Repository) Range(ctx context.Context, projectID string, from, to time.Time, limit int) ([]domain.Snapshot, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT ts, window_s, rps, p50, p95, p99, error_rate, unique_ips, top_ip_rps, avg_payload_bytes, endpoints_json
		 FROM metric_snapshots
		 WHERE project_id = ? AND ts >= ? AND ts <= ?
		 ORDER BY ts ASC
		 LIMIT ?`,
		projectID, from.UnixMilli(), to.UnixMilli(), limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	snapshots := make([]domain.Snapshot, 0)
	for rows.Next() {
		var (
			snapshot  domain.Snapshot
			tsMillis  int64
			endpoints string
		)
		if err := rows.Scan(
			&tsMillis, &snapshot.WindowS, &snapshot.RPS, &snapshot.P50, &snapshot.P95, &snapshot.P99,
			&snapshot.ErrorRate, &snapshot.UniqueIPs, &snapshot.TopIPRPS, &snapshot.AvgPayloadBytes, &endpoints,
		); err != nil {
			return nil, err
		}
		snapshot.Ts = time.UnixMilli(tsMillis).UTC()
		snapshot.Endpoints = make(map[string]int)
		_ = json.Unmarshal([]byte(endpoints), &snapshot.Endpoints)
		snapshots = append(snapshots, snapshot)
	}
	return snapshots, rows.Err()
}

func (r *Repository) Prune(ctx context.Context, before time.Time) (int64, error) {
	result, err := r.db.ExecContext(ctx,
		`DELETE FROM metric_snapshots WHERE ts < ?`, before.UnixMilli(),
	)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
