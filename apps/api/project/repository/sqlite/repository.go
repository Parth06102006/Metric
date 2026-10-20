package sqlite

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"strings"
	"time"

	"traffic-center/domain"
)

// Repository is the SQLite adapter for projects.
type Repository struct {
	db *sql.DB
}

// New creates a project repository.
func New(db *sql.DB) *Repository {
	return &Repository{db: db}
}

func (r *Repository) Create(ctx context.Context, project domain.Project, apiKeyHash string) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO projects (id, name, api_key_hash, created_at) VALUES (?, ?, ?, ?)`,
		project.ID, project.Name, apiKeyHash, time.Now().UnixMilli(),
	)
	if err != nil {
		if isUniqueViolation(err) {
			return domain.ErrConflict
		}
		return err
	}
	return nil
}

func (r *Repository) Get(ctx context.Context, projectID string) (domain.Project, error) {
	var project domain.Project
	err := r.db.QueryRowContext(ctx,
		`SELECT id, name FROM projects WHERE id = ?`, projectID,
	).Scan(&project.ID, &project.Name)

	if errors.Is(err, sql.ErrNoRows) {
		return domain.Project{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.Project{}, err
	}
	return project, nil
}

func (r *Repository) List(ctx context.Context) ([]domain.Project, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id, name FROM projects ORDER BY name ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	projects := make([]domain.Project, 0)
	for rows.Next() {
		var project domain.Project
		if err := rows.Scan(&project.ID, &project.Name); err != nil {
			return nil, err
		}
		projects = append(projects, project)
	}
	return projects, rows.Err()
}

// Authenticate compares the presented key against the stored hash in constant
// time. A missing project returns (false, nil) so callers cannot enumerate IDs.
func (r *Repository) Authenticate(ctx context.Context, projectID, apiKey string) (bool, error) {
	var storedHash string
	err := r.db.QueryRowContext(ctx,
		`SELECT api_key_hash FROM projects WHERE id = ?`, projectID,
	).Scan(&storedHash)

	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	provided := domain.HashAPIKey(apiKey)
	return subtle.ConstantTimeCompare([]byte(storedHash), []byte(provided)) == 1, nil
}

func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}
