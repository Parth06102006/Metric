// Package ops provides the standalone backup and monitoring service.
package ops

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	sqlite "modernc.org/sqlite"
)

// readOnlyDB never creates or migrates the database being inspected.
func readOnlyDB(path string) (*sql.DB, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("database is unavailable: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return nil, errors.New("database must be a non-empty regular file")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	u := url.URL{Scheme: "file", Path: abs}
	q := u.Query()
	q.Set("mode", "ro")
	q.Add("_pragma", "busy_timeout(5000)")
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err = db.Ping(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func checkSchema(ctx context.Context, db *sql.DB) error {
	var count int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name IN ('projects','metric_snapshots','anomalies','audit_events')`).Scan(&count); err != nil {
		return fmt.Errorf("read schema: %w", err)
	}
	if count != 4 {
		return errors.New("database does not contain the expected Metric schema")
	}
	return nil
}

// VerifySnapshot checks all reported integrity results and the expected schema.
func VerifySnapshot(ctx context.Context, path string) error {
	db, err := readOnlyDB(path)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	rows, err := db.QueryContext(ctx, "PRAGMA integrity_check")
	if err != nil {
		return fmt.Errorf("integrity check: %w", err)
	}
	defer func() { _ = rows.Close() }()
	count := 0
	for rows.Next() {
		var result string
		if err := rows.Scan(&result); err != nil {
			return err
		}
		count++
		if result != "ok" {
			return fmt.Errorf("integrity check failed: %s", result)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if count != 1 {
		return errors.New("integrity check returned no valid result")
	}
	if err := rows.Close(); err != nil {
		return err
	}
	return checkSchema(ctx, db)
}

// Snapshot uses SQLite's online backup API, including committed WAL content.
// The destination must be a fresh private temporary file, never the source.
func Snapshot(ctx context.Context, source, destination string) error {
	src, err := filepath.Abs(source)
	if err != nil {
		return err
	}
	dst, err := filepath.Abs(destination)
	if err != nil {
		return err
	}
	if src == dst {
		return errors.New("refusing to overwrite source database")
	}
	if sourceInfo, sourceErr := os.Stat(src); sourceErr == nil {
		if destInfo, destErr := os.Stat(dst); destErr == nil && os.SameFile(sourceInfo, destInfo) {
			return errors.New("refusing to overwrite an alias of the source database")
		}
	}
	db, err := readOnlyDB(src)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	if err := checkSchema(ctx, db); err != nil {
		return err
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	err = conn.Raw(func(raw any) error {
		driver, ok := raw.(interface {
			NewBackup(string) (*sqlite.Backup, error)
		})
		if !ok {
			return errors.New("SQLite driver does not support online backup")
		}
		backup, err := driver.NewBackup(dst)
		if err != nil {
			return err
		}
		var stepErr error
		for {
			if err := ctx.Err(); err != nil {
				stepErr = err
				break
			}
			more, err := backup.Step(256)
			if err != nil {
				stepErr = err
				break
			}
			if !more {
				break
			}
		}
		finishErr := backup.Finish()
		return errors.Join(stepErr, finishErr)
	})
	if err != nil {
		return fmt.Errorf("online backup: %w", err)
	}
	return VerifySnapshot(ctx, dst)
}

func hashFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return fmt.Sprintf("%x", h.Sum(nil)), n, nil
}

// KeyPrefix prevents accidental broad bucket scans and isolates the job's objects.
func KeyPrefix(prefix string) (string, error) {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		return "", errors.New("backup prefix cannot be empty")
	}
	if strings.HasPrefix(prefix, "/") || strings.Contains(prefix, "..") || strings.ContainsAny(prefix, "\\\n\r") {
		return "", errors.New("invalid backup prefix")
	}
	return strings.TrimRight(prefix, "/") + "/", nil
}
