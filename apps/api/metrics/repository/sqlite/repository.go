package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"traffic-center/domain"
)

// Cap total endpoint JSON materialized by a history/baseline read. This does
// not cap SQLite disk usage or all Go allocations.
const maxHistoryEndpointBytes = 4 << 20

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
		`SELECT ts, window_s, rps, p50, p95, p99, error_rate, unique_ips, top_ip_rps, avg_payload_bytes, substr(CAST(endpoints_json AS BLOB), 1, ?)
		 FROM metric_snapshots
		 WHERE project_id = ? AND ts >= ? AND ts <= ?
		 ORDER BY ts ASC
		 LIMIT ?`,
		maxHistoryEndpointBytes+1, projectID, from.UnixMilli(), to.UnixMilli(), limit,
	)
	if err != nil {
		return nil, err
	}
	return readSnapshots(rows, false)
}

// Recent limits the newest rows before returning them in chronological order.
func (r *Repository) Recent(ctx context.Context, projectID string, limit int) ([]domain.Snapshot, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT ts, window_s, rps, p50, p95, p99, error_rate, unique_ips, top_ip_rps, avg_payload_bytes,
		 substr(CAST(endpoints_json AS BLOB), 1, ?)
		 FROM metric_snapshots WHERE project_id = ? ORDER BY ts DESC, id DESC LIMIT ?`, maxHistoryEndpointBytes+1, projectID, limit,
	)
	if err != nil {
		return nil, err
	}
	return readSnapshots(rows, true)
}

func readSnapshots(rows *sql.Rows, newestFirst bool) ([]domain.Snapshot, error) {
	defer func() { _ = rows.Close() }()
	snapshots := make([]domain.Snapshot, 0)
	bytes := 0
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
		bytes += len(endpoints) + 256
		if bytes > maxHistoryEndpointBytes {
			if !newestFirst || len(snapshots) == 0 {
				return nil, domain.ErrCapacity
			}
			break // Return a contiguous suffix of newest history, never older rows.
		}
		snapshot.Ts = time.UnixMilli(tsMillis).UTC()
		snapshot.Endpoints = make(map[string]int)
		_ = json.Unmarshal([]byte(endpoints), &snapshot.Endpoints)
		snapshots = append(snapshots, snapshot)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if newestFirst {
		for i, j := 0, len(snapshots)-1; i < j; i, j = i+1, j-1 {
			snapshots[i], snapshots[j] = snapshots[j], snapshots[i]
		}
	}
	return snapshots, nil
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
