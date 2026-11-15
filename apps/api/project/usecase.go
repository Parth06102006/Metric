package project

import (
	"context"
	"strings"

	"traffic-center/domain"
)

const maxNameLength = 100

// Usecase implements project creation and lookup.
type Usecase struct {
	repo domain.ProjectRepository
}

// NewUsecase wires the project usecase to its repository port.
func NewUsecase(repo domain.ProjectRepository) *Usecase {
	return &Usecase{repo: repo}
}

// Create validates the name, mints an ID and an API key, and persists the
// hash. The plaintext key is returned to the caller exactly once.
func (u *Usecase) Create(ctx context.Context, name string) (domain.ProjectWithKey, error) {
	identity, err := domain.RequireAdmin(ctx)
	if err != nil {
		return domain.ProjectWithKey{}, err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return domain.ProjectWithKey{}, domain.Validationf("project name is required")
	}
	if len(name) > maxNameLength {
		return domain.ProjectWithKey{}, domain.Validationf("project name must be %d characters or fewer", maxNameLength)
	}

	id, err := domain.NewID(8)
	if err != nil {
		return domain.ProjectWithKey{}, err
	}

	key, err := domain.NewID(32)
	if err != nil {
		return domain.ProjectWithKey{}, err
	}

	project := domain.Project{ID: id, Name: name, OwnerEmail: identity.Email}
	if err := u.repo.Create(ctx, project, domain.HashAPIKey(key)); err != nil {
		return domain.ProjectWithKey{}, err
	}

	return domain.ProjectWithKey{Project: project, APIKey: key}, nil
}

// Get returns a project only to an authenticated owner, viewer or admin.
func (u *Usecase) Get(ctx context.Context, projectID string) (domain.Project, error) {
	identity, err := domain.RequireIdentity(ctx)
	if err != nil {
		return domain.Project{}, err
	}
	if strings.TrimSpace(projectID) == "" {
		return domain.Project{}, domain.Validationf("project id is required")
	}
	p, err := u.repo.Get(ctx, projectID)
	if err != nil {
		return domain.Project{}, err
	}
	if !p.CanAccess(identity) {
		return domain.Project{}, domain.ErrAccessDenied
	}
	return p, nil
}

// KnownUsers lists identities observed at a trusted sign-in; delivery is admin-only.
func (u *Usecase) KnownUsers(ctx context.Context) ([]string, error) {
	if _, err := domain.RequireAdmin(ctx); err != nil {
		return nil, err
	}
	return u.repo.ListUsers(ctx)
}

// Viewers returns project members; delivery is admin-only.
func (u *Usecase) Viewers(ctx context.Context, projectID string) ([]string, error) {
	if _, err := domain.RequireAdmin(ctx); err != nil {
		return nil, err
	}
	if _, err := u.repo.Get(ctx, projectID); err != nil {
		return nil, err
	}
	return u.repo.ListMembers(ctx, projectID)
}

// List returns projects accessible to the caller. Admins see all projects
// (including legacy ownerless projects); non-admins see only projects they own.
func (u *Usecase) List(ctx context.Context) ([]domain.Project, error) {
	identity, err := domain.RequireIdentity(ctx)
	if err != nil {
		return nil, err
	}
	if identity.Admin {
		return u.repo.List(ctx)
	}
	return u.repo.ListByOwner(ctx, identity.Email)
}
