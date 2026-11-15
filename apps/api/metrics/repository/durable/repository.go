// Package durable journals admitted live batches before publishing them in memory.
package durable

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
	"traffic-center/domain"
	"traffic-center/metrics/repository/memory"
)

// Repository is intended for a single API process owning the SQLite file.
type Repository struct {
	db        *sql.DB
	memory    *memory.Repository
	window    time.Duration
	diskDir   string
	minFree   uint64
	available func(string) (uint64, error)
}

func New(db *sql.DB, live *memory.Repository, window time.Duration, minFree uint64) (*Repository, error) {
	var seq int
	var name, path string
	if err := db.QueryRow(`PRAGMA database_list`).Scan(&seq, &name, &path); err != nil {
		return nil, fmt.Errorf("database path: %w", err)
	}
	if path == "" {
		return nil, errors.New("durable live journal requires an on-disk database")
	}
	if window <= 0 {
		return nil, errors.New("invalid live window")
	}
	return &Repository{db: db, memory: live, window: window, diskDir: filepath.Dir(path), minFree: minFree, available: freeBytes}, nil
}
func freeBytes(dir string) (uint64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		return 0, err
	}
	return st.Bavail * uint64(st.Bsize), nil
}

func (r *Repository) Append(ctx context.Context, id string, records []domain.LogRecord) error {
	return r.memory.AppendCommitted(ctx, id, records, func(batch []domain.LogRecord) error {
		data, err := json.Marshal(batch)
		if err != nil {
			return err
		}
		if len(data) > 8<<20 {
			return domain.ErrCapacity
		}
		var latest int64
		for _, v := range batch {
			if n := v.Ts.UnixNano(); n > latest {
				latest = n
			}
		}
		// BEGIN IMMEDIATE serializes our capacity check with other SQLite writers.
		tx, err := r.db.BeginTx(ctx, nil)
		if err != nil {
			return storageError(ctx, err)
		}
		defer func() { _ = tx.Rollback() }()
		free, err := r.available(r.diskDir)
		if err != nil {
			return fmt.Errorf("%w: disk space check failed", domain.ErrStorageUnavailable)
		}
		// Leave space for SQLite's WAL, indexes and unrelated operations. This is a
		// guard, not an OS quota; other processes can consume the same free space.
		overhead := uint64(len(data))*4 + (1 << 20)
		if free < r.minFree || free-r.minFree < overhead {
			return fmt.Errorf("%w: low disk space", domain.ErrStorageUnavailable)
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO live_batches(project_id,latest_ns,records_json) VALUES(?,?,?)`, id, latest, data); err != nil {
			return storageError(ctx, err)
		}
		if err = tx.Commit(); err != nil {
			return storageError(ctx, err)
		}
		return nil
	})
}
func storageError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return fmt.Errorf("%w: journal write: %v", domain.ErrStorageUnavailable, err)
}
func (r *Repository) Recent(ctx context.Context, id string, since time.Time) ([]domain.LogRecord, error) {
	return r.memory.Recent(ctx, id, since)
}
func (r *Repository) ActiveProjects(ctx context.Context) []string {
	return r.memory.ActiveProjects(ctx)
}
func (r *Repository) Delete(ctx context.Context, id string) error {
	if _, err := r.db.ExecContext(ctx, `DELETE FROM live_batches WHERE project_id=?`, id); err != nil {
		return storageError(ctx, err)
	}
	return r.memory.Delete(ctx, id)
}

// Recover runs before serving or starting workers. It fails closed if configured
// memory limits no longer accommodate records accepted before the restart.
func (r *Repository) Recover(ctx context.Context) error {
	rows, err := r.db.QueryContext(ctx, `SELECT project_id,records_json FROM live_batches WHERE latest_ns>=? ORDER BY id`, time.Now().Add(-r.window).UnixNano())
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id string
		var data []byte
		if err := rows.Scan(&id, &data); err != nil {
			return err
		}
		if len(data) > 8<<20 {
			return errors.New("live journal batch exceeds recovery size limit")
		}
		var batch []domain.LogRecord
		if err := json.Unmarshal(data, &batch); err != nil {
			return fmt.Errorf("decode live journal: %w", err)
		}
		for {
			cutoff := time.Now().Add(-r.window)
			active := make([]domain.LogRecord, 0, len(batch))
			for _, v := range batch {
				if !v.Ts.Before(cutoff) {
					active = append(active, v)
				}
			}
			if len(active) == 0 {
				break
			}
			err := r.memory.Append(ctx, id, active)
			if err == nil {
				break
			}
			// A record may expire while waiting for a lock or processing a long recovery.
			expired := false
			if errors.Is(err, domain.ErrValidation) {
				for _, record := range active {
					if record.Ts.Before(time.Now().Add(-r.window)) {
						expired = true
						break
					}
				}
			}
			if expired {
				batch = active
				continue
			}
			return fmt.Errorf("recover live journal (check clock and configured live limits): %w", err)
		}
	}
	return rows.Err()
}

// Ready checks the database connection, filesystem reserve and page budget.
// It is a point-in-time signal, not a guarantee that the next write will work.
func (r *Repository) Ready(ctx context.Context) error {
	if err := r.db.PingContext(ctx); err != nil {
		return err
	}
	free, err := r.available(r.diskDir)
	if err != nil {
		return err
	}
	if free < r.minFree+(1<<20) {
		return domain.ErrStorageUnavailable
	}
	var pages, limit, freePages int64
	if err := r.db.QueryRowContext(ctx, `PRAGMA page_count`).Scan(&pages); err != nil {
		return err
	}
	if err := r.db.QueryRowContext(ctx, `PRAGMA max_page_count`).Scan(&limit); err != nil {
		return err
	}
	if err := r.db.QueryRowContext(ctx, `PRAGMA freelist_count`).Scan(&freePages); err != nil {
		return err
	}
	if pages >= limit && freePages == 0 {
		return domain.ErrStorageUnavailable
	}
	return nil
}

// Prune removes bounded batches that contain no still-live record.
func (r *Repository) Prune(ctx context.Context) (int64, error) {
	result, err := r.db.ExecContext(ctx, `DELETE FROM live_batches WHERE id IN (SELECT id FROM live_batches WHERE latest_ns < ? ORDER BY latest_ns LIMIT 16)`, time.Now().Add(-r.window).UnixNano())
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
func (r *Repository) pruneExpired(ctx context.Context) error {
	for ctx.Err() == nil {
		count, err := r.Prune(ctx)
		if err != nil {
			return err
		}
		if count < 16 {
			return nil
		}
	}
	return ctx.Err()
}

func (r *Repository) RunPruner(ctx context.Context, logger *slog.Logger) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Keep transactions small, but drain a backlog instead of limiting
			// cleanup to a fixed number of batches per interval.
			if err := r.pruneExpired(ctx); err != nil && ctx.Err() == nil {
				logger.Error("prune live journal", "error", err)
			}
		}
	}
}
