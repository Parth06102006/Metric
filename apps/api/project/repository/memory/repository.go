package memory

import (
	"context"
	"crypto/subtle"
	"sort"
	"sync"

	"traffic-center/domain"
)

// Repository is an in-memory ProjectRepository for usecase tests.
type Repository struct {
	mu       sync.RWMutex
	projects map[string]domain.Project
	hashes   map[string]string
}

// New creates an empty repository.
func New() *Repository {
	return &Repository{
		projects: make(map[string]domain.Project),
		hashes:   make(map[string]string),
	}
}

func (r *Repository) Create(_ context.Context, project domain.Project, apiKeyHash string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.projects[project.ID]; exists {
		return domain.ErrConflict
	}
	r.projects[project.ID] = project
	r.hashes[project.ID] = apiKeyHash
	return nil
}

func (r *Repository) Get(_ context.Context, projectID string) (domain.Project, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	project, ok := r.projects[projectID]
	if !ok {
		return domain.Project{}, domain.ErrNotFound
	}
	return project, nil
}

func (r *Repository) List(_ context.Context) ([]domain.Project, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	projects := make([]domain.Project, 0, len(r.projects))
	for _, project := range r.projects {
		projects = append(projects, project)
	}
	sort.Slice(projects, func(i, j int) bool { return projects[i].Name < projects[j].Name })
	return projects, nil
}

func (r *Repository) Authenticate(_ context.Context, projectID, apiKey string) (bool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	hash, ok := r.hashes[projectID]
	if !ok {
		return false, nil
	}
	return subtle.ConstantTimeCompare([]byte(hash), []byte(domain.HashAPIKey(apiKey))) == 1, nil
}
