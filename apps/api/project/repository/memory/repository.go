package memory

import (
	"context"
	"crypto/subtle"
	"sort"
	"strings"
	"sync"

	"traffic-center/domain"
)

// Repository is an in-memory ProjectRepository for usecase tests.
type Repository struct {
	mu       sync.RWMutex
	projects map[string]domain.Project
	hashes   map[string]string
	revoked  map[string]bool
}

// New creates an empty repository.
func New() *Repository {
	return &Repository{
		projects: make(map[string]domain.Project),
		hashes:   make(map[string]string),
		revoked:  make(map[string]bool),
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

func (r *Repository) ListByOwner(_ context.Context, ownerEmail string) ([]domain.Project, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	projects := make([]domain.Project, 0)
	for _, project := range r.projects {
		if strings.EqualFold(project.OwnerEmail, ownerEmail) {
			projects = append(projects, project)
		}
	}
	sort.Slice(projects, func(i, j int) bool { return projects[i].Name < projects[j].Name })
	return projects, nil
}

func (r *Repository) Authenticate(_ context.Context, projectID, apiKey string) (bool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if r.revoked[projectID] {
		return false, nil
	}
	hash, ok := r.hashes[projectID]
	if !ok {
		return false, nil
	}
	return subtle.ConstantTimeCompare([]byte(hash), []byte(domain.HashAPIKey(apiKey))) == 1, nil
}

// UpdateKeyHash replaces the stored key hash and re-activates a revoked key.
func (r *Repository) UpdateKeyHash(_ context.Context, projectID, apiKeyHash string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.projects[projectID]; !exists {
		return domain.ErrNotFound
	}
	r.hashes[projectID] = apiKeyHash
	r.revoked[projectID] = false
	return nil
}

// RevokeKey marks the ingest key invalid.
func (r *Repository) RevokeKey(_ context.Context, projectID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.projects[projectID]; !exists {
		return domain.ErrNotFound
	}
	r.revoked[projectID] = true
	return nil
}

// Delete removes a project entirely.
func (r *Repository) Delete(_ context.Context, projectID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.projects[projectID]; !exists {
		return domain.ErrNotFound
	}
	delete(r.projects, projectID)
	delete(r.hashes, projectID)
	delete(r.revoked, projectID)
	return nil
}
