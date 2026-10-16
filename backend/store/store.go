package store

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"traffic-center/backend/metrics"
)

var ErrNotFound = errors.New("not found")

type Store struct {
	DB *sql.DB
}

type Project struct {
	ID   string `json:"project_id"`
	Name string `json:"name"`
}

type Thresholds struct {
	P95Ms       float64 `json:"p95_ms"`
	ErrorRate   float64 `json:"error_rate"`
	RPSMax      float64 `json:"rps_max"`
	ReqPerIPMax float64 `json:"req_per_ip_max"`
}

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}

	db.SetMaxOpenConns(1)

	s := &Store{DB: db}
	if err := s.init(); err != nil {
		db.Close()
		return nil, err
	}

	return s, nil
}

func (s *Store) Close() error {
	return s.DB.Close()
}

func (s *Store) init() error {
	schema := `
	CREATE TABLE IF NOT EXISTS projects (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		api_key_hash TEXT NOT NULL,
		created_at TEXT NOT NULL
	);

	CREATE TABLE IF NOT EXISTS thresholds (
		project_id TEXT PRIMARY KEY,
		p95_ms REAL NOT NULL DEFAULT 1000,
		error_rate REAL NOT NULL DEFAULT 5,
		rps_max REAL NOT NULL DEFAULT 100,
		req_per_ip_max REAL NOT NULL DEFAULT 30,
		FOREIGN KEY(project_id) REFERENCES projects(id)
	);

	CREATE TABLE IF NOT EXISTS metric_snapshots (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		project_id TEXT NOT NULL,
		created_at TEXT NOT NULL,
		data_json TEXT NOT NULL
	);

	CREATE TABLE IF NOT EXISTS anomalies (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		project_id TEXT NOT NULL,
		created_at TEXT NOT NULL,
		severity TEXT NOT NULL,
		summary TEXT NOT NULL,
		analysis TEXT NOT NULL
	);

	CREATE TABLE IF NOT EXISTS alerts (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		project_id TEXT NOT NULL,
		created_at TEXT NOT NULL,
		message TEXT NOT NULL,
		status TEXT NOT NULL DEFAULT 'open'
	);
	`

	_, err := s.DB.Exec(schema)
	return err
}

func newID(bytesCount int) (string, error) {
	b := make([]byte, bytesCount)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func hashKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

func (s *Store) CreateProject(name string) (Project, string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Project{}, "", errors.New("project name is required")
	}
	if len(name) > 100 {
		return Project{}, "", errors.New("project name must be 100 characters or fewer")
	}

	id, err := newID(8)
	if err != nil {
		return Project{}, "", err
	}

	key, err := newID(32)
	if err != nil {
		return Project{}, "", err
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)

	tx, err := s.DB.Begin()
	if err != nil {
		return Project{}, "", err
	}
	defer tx.Rollback()

	_, err = tx.Exec(
		`INSERT INTO projects (id, name, api_key_hash, created_at) VALUES (?, ?, ?, ?)`,
		id, name, hashKey(key), now,
	)
	if err != nil {
		return Project{}, "", err
	}

	_, err = tx.Exec(
		`INSERT INTO thresholds (project_id) VALUES (?)`,
		id,
	)
	if err != nil {
		return Project{}, "", err
	}

	if err := tx.Commit(); err != nil {
		return Project{}, "", err
	}

	return Project{ID: id, Name: name}, key, nil
}

func (s *Store) AuthenticateProject(projectID, key string) (bool, error) {
	var storedHash string
	err := s.DB.QueryRow(
		`SELECT api_key_hash FROM projects WHERE id = ?`,
		projectID,
	).Scan(&storedHash)

	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrNotFound
	}
	if err != nil {
		return false, err
	}

	providedHash := hashKey(key)
	return subtle.ConstantTimeCompare([]byte(storedHash), []byte(providedHash)) == 1, nil
}

func (s *Store) GetProject(projectID string) (Project, error) {
	var p Project
	err := s.DB.QueryRow(
		`SELECT id, name FROM projects WHERE id = ?`,
		projectID,
	).Scan(&p.ID, &p.Name)

	if errors.Is(err, sql.ErrNoRows) {
		return Project{}, ErrNotFound
	}
	if err != nil {
		return Project{}, err
	}

	return p, nil
}

func (s *Store) GetThresholds(projectID string) (Thresholds, error) {
	var t Thresholds
	err := s.DB.QueryRow(
		`SELECT p95_ms, error_rate, rps_max, req_per_ip_max
		 FROM thresholds WHERE project_id = ?`,
		projectID,
	).Scan(&t.P95Ms, &t.ErrorRate, &t.RPSMax, &t.ReqPerIPMax)

	if errors.Is(err, sql.ErrNoRows) {
		return Thresholds{}, ErrNotFound
	}
	if err != nil {
		return Thresholds{}, err
	}

	return t, nil
}

func (s *Store) UpdateThresholds(projectID string, t Thresholds) error {
	if t.P95Ms < 0 || t.ErrorRate < 0 || t.ErrorRate > 100 ||
		t.RPSMax < 0 || t.ReqPerIPMax < 0 {
		return errors.New("thresholds must be non-negative and error_rate must not exceed 100")
	}

	result, err := s.DB.Exec(
		`UPDATE thresholds
		 SET p95_ms = ?, error_rate = ?, rps_max = ?, req_per_ip_max = ?
		 WHERE project_id = ?`,
		t.P95Ms, t.ErrorRate, t.RPSMax, t.ReqPerIPMax, projectID,
	)
	if err != nil {
		return err
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		var exists int
		err := s.DB.QueryRow(
			`SELECT 1 FROM projects WHERE id = ?`,
			projectID,
		).Scan(&exists)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("check project existence: %w", err)
		}
	}

	return nil
}

// SaveSnapshot writes a JSON-serialized metric snapshot to metric_snapshots.
func (s *Store) SaveSnapshot(projectID string, snapshot metrics.Snapshot) error {
	dataJSON, err := json.Marshal(snapshot)
	if err != nil {
		return fmt.Errorf("marshal snapshot: %w", err)
	}

	createdAt := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = s.DB.Exec(
		`INSERT INTO metric_snapshots (project_id, created_at, data_json) VALUES (?, ?, ?)`,
		projectID, createdAt, string(dataJSON),
	)
	return err
}

// AnomalyRecord represents an anomaly to be stored in the database.
type AnomalyRecord struct {
	Type       string
	Severity   string
	Message    string
	Value      float64
	Threshold  float64
	Analysis   string
	DetectedAt time.Time
}

// RecordAnomaly stores a detected anomaly in the anomalies table.
func (s *Store) RecordAnomaly(projectID string, a AnomalyRecord) error {
	createdAt := a.DetectedAt.UTC().Format(time.RFC3339Nano)
	if a.DetectedAt.IsZero() {
		createdAt = time.Now().UTC().Format(time.RFC3339Nano)
	}

	analysis := a.Analysis
	if strings.TrimSpace(analysis) == "" {
		analysis = fmt.Sprintf("threshold=%.2f, current=%.2f", a.Threshold, a.Value)
	}

	_, err := s.DB.Exec(
		`INSERT INTO anomalies (project_id, created_at, severity, summary, analysis) VALUES (?, ?, ?, ?, ?)`,
		projectID, createdAt, a.Severity, a.Type+": "+a.Message, analysis,
	)
	return err
}

// HasRecentAnomaly checks if an anomaly of the given type was already recorded for the project within withinDur.
func (s *Store) HasRecentAnomaly(projectID, anomalyType string, withinDur time.Duration) (bool, error) {
	cutoff := time.Now().UTC().Add(-withinDur).Format(time.RFC3339Nano)
	prefixPattern := anomalyType + ":%"

	var count int
	err := s.DB.QueryRow(
		`SELECT COUNT(1) FROM anomalies WHERE project_id = ? AND summary LIKE ? AND created_at >= ?`,
		projectID, prefixPattern, cutoff,
	).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}
