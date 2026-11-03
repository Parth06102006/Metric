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
	project.Viewers, err = r.ListMembers(ctx, projectID)
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
	defer func() { _ = rows.Close() }()

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
		`SELECT id, name, owner_email FROM projects p
		 WHERE lower(owner_email) = lower(?) OR EXISTS (
		   SELECT 1 FROM project_viewers v WHERE v.project_id = p.id AND v.email = ? COLLATE NOCASE
		 ) ORDER BY name ASC`,
		ownerEmail, ownerEmail,
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

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

// RecordUser is called only after the trusted identity middleware succeeds.
func (r *Repository) RecordUser(ctx context.Context, email string) error {
	_, err := r.db.ExecContext(ctx, `INSERT INTO known_users(email) VALUES (?) ON CONFLICT(email) DO NOTHING`, email)
	return err
}

func (r *Repository) ListUsers(ctx context.Context) ([]string, error) {
	return r.strings(ctx, `SELECT email FROM known_users ORDER BY email COLLATE NOCASE`)
}

func (r *Repository) ListMembers(ctx context.Context, projectID string) ([]string, error) {
	return r.strings(ctx, `SELECT email FROM project_viewers WHERE project_id = ? ORDER BY email COLLATE NOCASE`, projectID)
}

func (r *Repository) strings(ctx context.Context, query string, args ...any) ([]string, error) {
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	values := make([]string, 0)
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (r *Repository) AddMember(ctx context.Context, projectID, email string) error {
	// Both existence checks are part of the same write, avoiding a race with
	// deletion and ensuring only identities observed at sign-in can be added.
	result, err := r.db.ExecContext(ctx, `INSERT INTO project_viewers(project_id, email)
	 SELECT p.id, u.email FROM projects p JOIN known_users u ON u.email = ? COLLATE NOCASE
	 WHERE p.id = ? ON CONFLICT(project_id, email) DO NOTHING`, email, projectID)
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		var found int
		if err := r.db.QueryRowContext(ctx, `SELECT count(*) FROM project_viewers WHERE project_id = ? AND email = ? COLLATE NOCASE`, projectID, email).Scan(&found); err != nil {
			return err
		}
		if found == 0 {
			return domain.ErrNotFound
		}
	}
	return nil
}

func (r *Repository) RemoveMember(ctx context.Context, projectID, email string) error {
	result, err := r.db.ExecContext(ctx, `DELETE FROM project_viewers WHERE project_id = ? AND email = ? COLLATE NOCASE`, projectID, email)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return domain.ErrNotFound
	}
	return nil
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

// SetOwner assigns or clears the project owner.
func (r *Repository) SetOwner(ctx context.Context, projectID, ownerEmail string) error {
	result, err := r.db.ExecContext(ctx,
		`UPDATE projects SET owner_email = ? WHERE id = ?`, ownerEmail, projectID,
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
	defer func() { _ = tx.Rollback() }() // Commit makes this a no-op.

	for _, statement := range []string{
		`DELETE FROM project_viewers WHERE project_id = ?`,
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
