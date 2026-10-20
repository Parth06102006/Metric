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

	project := domain.Project{ID: id, Name: name}
	if err := u.repo.Create(ctx, project, domain.HashAPIKey(key)); err != nil {
		return domain.ProjectWithKey{}, err
	}

	return domain.ProjectWithKey{Project: project, APIKey: key}, nil
}

// Get returns a project or domain.ErrNotFound.
func (u *Usecase) Get(ctx context.Context, projectID string) (domain.Project, error) {
	if strings.TrimSpace(projectID) == "" {
		return domain.Project{}, domain.Validationf("project id is required")
	}
	return u.repo.Get(ctx, projectID)
}

// List returns all projects.
func (u *Usecase) List(ctx context.Context) ([]domain.Project, error) {
	return u.repo.List(ctx)
}
