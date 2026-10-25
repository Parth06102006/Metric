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

	var ownerEmail string
	if identity, ok := domain.IdentityFrom(ctx); ok {
		ownerEmail = identity.Email
	}

	project := domain.Project{ID: id, Name: name, OwnerEmail: ownerEmail}
	if err := u.repo.Create(ctx, project, domain.HashAPIKey(key)); err != nil {
		return domain.ProjectWithKey{}, err
	}

	return domain.ProjectWithKey{Project: project, APIKey: key}, nil
}

// Get returns a project or domain.ErrNotFound. If caller identity is present,
// it enforces that only the owner or an admin may access the project.
func (u *Usecase) Get(ctx context.Context, projectID string) (domain.Project, error) {
	if strings.TrimSpace(projectID) == "" {
		return domain.Project{}, domain.Validationf("project id is required")
	}
	p, err := u.repo.Get(ctx, projectID)
	if err != nil {
		return domain.Project{}, err
	}
	if identity, ok := domain.IdentityFrom(ctx); ok && !p.CanAccess(identity) {
		return domain.Project{}, domain.ErrNotFound
	}
	return p, nil
}

// List returns projects accessible to the caller. Admins see all projects
// (including legacy ownerless projects); non-admins see only projects they own.
func (u *Usecase) List(ctx context.Context) ([]domain.Project, error) {
	identity, ok := domain.IdentityFrom(ctx)
	if !ok || identity.Admin {
		return u.repo.List(ctx)
	}
	return u.repo.ListByOwner(ctx, identity.Email)
}
