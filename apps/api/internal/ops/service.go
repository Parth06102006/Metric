package ops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

const reserveBytes int64 = 128 << 20

type Status struct {
	LastSuccess               int64 `json:"last_success"`
	LastAttempt               int64 `json:"last_attempt"`
	LastAttemptSuccess        bool  `json:"last_attempt_success"`
	LastRestoreSuccess        int64 `json:"last_restore_success"`
	LastRestoreAttempt        int64 `json:"last_restore_attempt"`
	LastRestoreAttemptSuccess bool  `json:"last_restore_attempt_success"`
}

type Service struct {
	Database, WorkDir, StatePath string
	APIReadyURL                  string
	Store                        *Store
	Logger                       *slog.Logger
	mu                           sync.RWMutex
	status                       Status
	inProgress                   bool
	restoreInProgress            bool
}

func NewService(db, dir string, store *Store, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	s := &Service{Database: db, WorkDir: dir, StatePath: filepath.Join(dir, "metric-backup-status.json"), Store: store, Logger: logger}
	data, err := os.ReadFile(s.StatePath)
	if err == nil {
		if err := json.Unmarshal(data, &s.status); err != nil {
			s.status = Status{}
			logger.Error("invalid backup state", "error", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		logger.Error("cannot read backup state", "error", err)
	}
	return s
}

func filesystem(path string) (available, total uint64, err error) {
	var fs unix.Statfs_t
	if err = unix.Statfs(path, &fs); err != nil {
		return 0, 0, err
	}
	return fs.Bavail * uint64(fs.Bsize), fs.Blocks * uint64(fs.Bsize), nil
}

func requireSpace(dir string, needed int64) error {
	if needed < 0 || needed > int64(^uint64(0)>>1)-reserveBytes {
		return errors.New("invalid estimated backup size")
	}
	available, _, err := filesystem(dir)
	if err != nil {
		return fmt.Errorf("check backup free space: %w", err)
	}
	if available < uint64(needed+reserveBytes) {
		return fmt.Errorf("insufficient free space for backup plus %d byte reserve", reserveBytes)
	}
	return nil
}

func (s *Service) saveStatus(status Status) error {
	data, err := json.Marshal(status)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(s.WorkDir, ".metric-backup-status-")
	if err != nil {
		return err
	}
	name := f.Name()
	defer func() { _ = os.Remove(name) }()
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err = errors.Join(err, closeErr); err != nil {
		return err
	}
	return os.Rename(name, s.StatePath)
}

func (s *Service) update(status Status) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.saveStatus(status); err != nil {
		// Do not report success if the persistent status could not be saved.
		s.status.LastAttempt = status.LastAttempt
		s.status.LastAttemptSuccess = false
		s.status.LastRestoreAttemptSuccess = false
		return err
	}
	s.status = status
	return nil
}
func (s *Service) current() Status { s.mu.RLock(); defer s.mu.RUnlock(); return s.status }

func (s *Service) Backup(ctx context.Context) error {
	s.mu.Lock()
	if s.inProgress {
		s.mu.Unlock()
		return errors.New("backup already in progress")
	}
	s.inProgress = true
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.inProgress = false; s.mu.Unlock() }()
	lock, err := os.OpenFile(filepath.Join(s.WorkDir, ".metric-backup.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return fmt.Errorf("open backup lock: %w", err)
	}
	defer func() { _ = lock.Close() }()
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return fmt.Errorf("another backup is running: %w", err)
	}
	defer func() { _ = unix.Flock(int(lock.Fd()), unix.LOCK_UN) }()
	status := s.current()
	status.LastAttempt = time.Now().Unix()
	status.LastAttemptSuccess = false
	if err := s.update(status); err != nil {
		return fmt.Errorf("record backup attempt: %w", err)
	}
	if s.Store == nil {
		return errors.New("R2 backup is not configured")
	}
	info, err := os.Stat(s.Database)
	if err != nil {
		return fmt.Errorf("stat source database: %w", err)
	}
	estimate := info.Size()
	if wal, e := os.Stat(s.Database + "-wal"); e == nil {
		estimate += wal.Size()
	} else if !errors.Is(e, os.ErrNotExist) {
		return e
	}
	if err := requireSpace(s.WorkDir, estimate); err != nil {
		return err
	}
	work, err := os.MkdirTemp(s.WorkDir, ".metric-backup-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(work) }()
	path := filepath.Join(work, "snapshot.db")
	// Pre-create privately: the SQLite driver will reuse this file.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := Snapshot(ctx, s.Database, path); err != nil {
		return err
	}
	key, err := s.Store.Upload(ctx, path)
	if err != nil {
		return err
	}
	status.LastSuccess = time.Now().Unix()
	status.LastAttemptSuccess = true
	if err := s.update(status); err != nil {
		return fmt.Errorf("record backup success: %w", err)
	}
	s.Logger.Info("backup uploaded and metadata verified", "key", key)
	return nil
}

// RestoreCheck validates the newest remote backup in an isolated temporary
// directory. It never installs or changes the production database.
func (s *Service) RestoreCheck(ctx context.Context) error {
	s.mu.Lock()
	if s.restoreInProgress {
		s.mu.Unlock()
		return errors.New("restore check already in progress")
	}
	s.restoreInProgress = true
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.restoreInProgress = false; s.mu.Unlock() }()
	status := s.current()
	status.LastRestoreAttempt = time.Now().Unix()
	status.LastRestoreAttemptSuccess = false
	if err := s.update(status); err != nil {
		return fmt.Errorf("record restore attempt: %w", err)
	}
	if s.Store == nil {
		return errors.New("R2 backup is not configured")
	}
	if err := s.Store.VerifyLatest(ctx, s.WorkDir); err != nil {
		return err
	}
	status.LastRestoreSuccess = time.Now().Unix()
	status.LastRestoreAttemptSuccess = true
	if err := s.update(status); err != nil {
		return fmt.Errorf("record restore success: %w", err)
	}
	s.Logger.Info("remote restore check passed")
	return nil
}

func (s *Service) Run(ctx context.Context, interval, timeout time.Duration) {
	run := func() bool {
		runCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		if err := s.Backup(runCtx); err != nil {
			s.Logger.Error("backup failed", "error", err)
			return false
		}
		lastRestore := s.current().LastRestoreSuccess
		if lastRestore == 0 || time.Since(time.Unix(lastRestore, 0)) >= 30*24*time.Hour {
			if err := s.RestoreCheck(runCtx); err != nil {
				s.Logger.Error("restore check failed", "error", err)
			}
		}
		return true
	}
	for {
		if ctx.Err() != nil {
			return
		}
		started := time.Now()
		wait := interval
		if !run() && wait > 5*time.Minute {
			wait = 5 * time.Minute
		}
		wait -= time.Since(started)
		if wait < 0 {
			wait = 0
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func fileSize(path string) (int64, bool) {
	info, err := os.Stat(path)
	if err == nil && info.Mode().IsRegular() {
		return info.Size(), true
	}
	return 0, false
}

// Metrics deliberately expose no project content, bucket names or secrets.
func (s *Service) Metrics(w http.ResponseWriter, request *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	avail, total, err := filesystem(filepath.Dir(s.Database))
	fsOK := 1
	if err != nil {
		fsOK = 0
	}
	db, dbOK := fileSize(s.Database)
	wal, walOK := fileSize(s.Database + "-wal")
	if !walOK {
		if _, err := os.Stat(s.Database + "-wal"); errors.Is(err, os.ErrNotExist) {
			walOK = true
		}
	}
	s.mu.RLock()
	status, inProgress, restoreInProgress := s.status, s.inProgress, s.restoreInProgress
	s.mu.RUnlock()
	flag := func(v bool) int {
		if v {
			return 1
		}
		return 0
	}
	_, _ = fmt.Fprintf(w, `# HELP metric_disk_stat_success Whether the database filesystem could be inspected.
# TYPE metric_disk_stat_success gauge
metric_disk_stat_success %d
# HELP metric_disk_available_bytes Bytes available to the service on the database filesystem.
# TYPE metric_disk_available_bytes gauge
metric_disk_available_bytes %d
# HELP metric_disk_total_bytes Total bytes on the database filesystem.
# TYPE metric_disk_total_bytes gauge
metric_disk_total_bytes %d
# HELP metric_database_stat_success Whether the SQLite database could be inspected.
# TYPE metric_database_stat_success gauge
metric_database_stat_success %d
# HELP metric_database_bytes SQLite database size in bytes.
# TYPE metric_database_bytes gauge
metric_database_bytes %d
# HELP metric_wal_stat_success Whether the SQLite WAL could be inspected; a missing WAL is valid.
# TYPE metric_wal_stat_success gauge
metric_wal_stat_success %d
# HELP metric_wal_bytes SQLite WAL size in bytes.
# TYPE metric_wal_bytes gauge
metric_wal_bytes %d
# HELP metric_backup_last_success_timestamp_seconds Time of the last verified successful upload, or zero.
# TYPE metric_backup_last_success_timestamp_seconds gauge
metric_backup_last_success_timestamp_seconds %d
# HELP metric_backup_last_attempt_timestamp_seconds Time of the last backup attempt, or zero.
# TYPE metric_backup_last_attempt_timestamp_seconds gauge
metric_backup_last_attempt_timestamp_seconds %d
# HELP metric_backup_last_attempt_success Whether the last backup attempt succeeded.
# TYPE metric_backup_last_attempt_success gauge
metric_backup_last_attempt_success %d
# HELP metric_backup_in_progress Whether a backup attempt is currently running.
# TYPE metric_backup_in_progress gauge
metric_backup_in_progress %d
# HELP metric_backup_last_restore_success_timestamp_seconds Time of the last successful remote restore check, or zero.
# TYPE metric_backup_last_restore_success_timestamp_seconds gauge
metric_backup_last_restore_success_timestamp_seconds %d
# HELP metric_backup_last_restore_attempt_timestamp_seconds Time of the last restore check attempt, or zero.
# TYPE metric_backup_last_restore_attempt_timestamp_seconds gauge
metric_backup_last_restore_attempt_timestamp_seconds %d
# HELP metric_backup_last_restore_attempt_success Whether the last restore check succeeded.
# TYPE metric_backup_last_restore_attempt_success gauge
metric_backup_last_restore_attempt_success %d
# HELP metric_backup_restore_in_progress Whether a restore check is running.
# TYPE metric_backup_restore_in_progress gauge
metric_backup_restore_in_progress %d
`, fsOK, avail, total, flag(dbOK), db, flag(walOK), wal, status.LastSuccess, status.LastAttempt, flag(status.LastAttemptSuccess), flag(inProgress), status.LastRestoreSuccess, status.LastRestoreAttempt, flag(status.LastRestoreAttemptSuccess), flag(restoreInProgress))
	s.applicationMetrics(w, request)
}
