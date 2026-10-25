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
		`INSERT INTO projects (id, name, api_key_hash, created_at, owner_email) VALUES (?, ?, ?, ?, ?)`,
		project.ID, project.Name, apiKeyHash, time.Now().UnixMilli(), project.OwnerEmail,
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
		`SELECT id, name, owner_email FROM projects WHERE id = ?`, projectID,
	).Scan(&project.ID, &project.Name, &project.OwnerEmail)

	if errors.Is(err, sql.ErrNoRows) {
		return domain.Project{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.Project{}, err
	}
	return project, nil
}

func (r *Repository) List(ctx context.Context) ([]domain.Project, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id, name, owner_email FROM projects ORDER BY name ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	projects := make([]domain.Project, 0)
	for rows.Next() {
		var project domain.Project
		if err := rows.Scan(&project.ID, &project.Name, &project.OwnerEmail); err != nil {
			return nil, err
		}
		projects = append(projects, project)
	}
	return projects, rows.Err()
}

func (r *Repository) ListByOwner(ctx context.Context, ownerEmail string) ([]domain.Project, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, name, owner_email FROM projects WHERE lower(owner_email) = lower(?) ORDER BY name ASC`,
		ownerEmail,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	projects := make([]domain.Project, 0)
	for rows.Next() {
		var project domain.Project
		if err := rows.Scan(&project.ID, &project.Name, &project.OwnerEmail); err != nil {
			return nil, err
		}
		projects = append(projects, project)
	}
	return projects, rows.Err()
}

// Authenticate compares the presented key against the stored hash in constant
// time. A missing or revoked project returns (false, nil) so callers cannot
// enumerate IDs or revocation state.
func (r *Repository) Authenticate(ctx context.Context, projectID, apiKey string) (bool, error) {
	var storedHash string
	err := r.db.QueryRowContext(ctx,
		`SELECT api_key_hash FROM projects WHERE id = ? AND api_key_revoked = 0`, projectID,
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

// UpdateKeyHash replaces the stored key hash and re-activates a revoked key.
func (r *Repository) UpdateKeyHash(ctx context.Context, projectID, apiKeyHash string) error {
	result, err := r.db.ExecContext(ctx,
		`UPDATE projects SET api_key_hash = ?, api_key_revoked = 0 WHERE id = ?`,
		apiKeyHash, projectID,
	)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// RevokeKey marks the ingest key invalid. Revoking an already revoked key is
// idempotent.
func (r *Repository) RevokeKey(ctx context.Context, projectID string) error {
	result, err := r.db.ExecContext(ctx,
		`UPDATE projects SET api_key_revoked = 1 WHERE id = ?`,
		projectID,
	)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// Delete removes the project and its dependent data in one transaction.
// Thresholds also cascade by foreign key, but the explicit deletes do not
// rely on that pragma being honoured by every connection.
func (r *Repository) Delete(ctx context.Context, projectID string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for _, statement := range []string{
		`DELETE FROM thresholds WHERE project_id = ?`,
		`DELETE FROM metric_snapshots WHERE project_id = ?`,
		`DELETE FROM anomalies WHERE project_id = ?`,
	} {
		if _, err := tx.ExecContext(ctx, statement, projectID); err != nil {
			return err
		}
	}

	result, err := tx.ExecContext(ctx, `DELETE FROM projects WHERE id = ?`, projectID)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return domain.ErrNotFound
	}

	return tx.Commit()
}
