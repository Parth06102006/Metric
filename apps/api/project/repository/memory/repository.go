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
	users    map[string]string
	members  map[string]map[string]string
}

// New creates an empty repository.
func New() *Repository {
	return &Repository{
		projects: make(map[string]domain.Project),
		hashes:   make(map[string]string),
		revoked:  make(map[string]bool),
		users:    make(map[string]string),
		members:  make(map[string]map[string]string),
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
	project.Viewers = make([]string, 0, len(r.members[projectID]))
	for _, email := range r.members[projectID] {
		project.Viewers = append(project.Viewers, email)
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
		_, member := r.members[project.ID][strings.ToLower(ownerEmail)]
		if strings.EqualFold(project.OwnerEmail, ownerEmail) || member {
			projects = append(projects, project)
		}
	}
	sort.Slice(projects, func(i, j int) bool { return projects[i].Name < projects[j].Name })
	return projects, nil
}

func (r *Repository) RecordUser(_ context.Context, email string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.users[strings.ToLower(email)] = email
	return nil
}

func (r *Repository) ListUsers(_ context.Context) ([]string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.users))
	for _, email := range r.users {
		out = append(out, email)
	}
	sort.Strings(out)
	return out, nil
}

func (r *Repository) ListMembers(_ context.Context, projectID string) ([]string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.members[projectID]))
	for _, email := range r.members[projectID] {
		out = append(out, email)
	}
	sort.Strings(out)
	return out, nil
}

func (r *Repository) AddMember(_ context.Context, projectID, email string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.projects[projectID]; !ok {
		return domain.ErrNotFound
	}
	known, ok := r.users[strings.ToLower(email)]
	if !ok {
		return domain.ErrNotFound
	}
	if r.members[projectID] == nil {
		r.members[projectID] = make(map[string]string)
	}
	r.members[projectID][strings.ToLower(email)] = known
	return nil
}

func (r *Repository) RemoveMember(_ context.Context, projectID, email string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := strings.ToLower(email)
	if _, ok := r.members[projectID][key]; !ok {
		return domain.ErrNotFound
	}
	delete(r.members[projectID], key)
	return nil
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

// SetOwner assigns or clears the project owner.
func (r *Repository) SetOwner(_ context.Context, projectID, ownerEmail string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	project, exists := r.projects[projectID]
	if !exists {
		return domain.ErrNotFound
	}
	project.OwnerEmail = ownerEmail
	r.projects[projectID] = project
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
	delete(r.members, projectID)
	return nil
}
