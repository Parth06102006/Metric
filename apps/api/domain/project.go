package domain

import "strings"

// Project is an ingest target owned by one tenant.
type Project struct {
	ID         string `json:"project_id"`
	Name       string `json:"name"`
	OwnerEmail string `json:"owner_email,omitempty"`
}

// CanAccess reports whether the caller identity may access this project.
// Admins may access all projects. Owners may access their own projects.
// Legacy projects with no owner (empty OwnerEmail) may only be accessed by admins.
func (p Project) CanAccess(identity Identity) bool {
	if identity.Admin {
		return true
	}
	return p.OwnerEmail != "" && strings.EqualFold(p.OwnerEmail, identity.Email)
}

// ProjectWithKey is returned exactly once, when the project is created.
// The plaintext API key is never persisted or returned again.
type ProjectWithKey struct {
	Project
	APIKey string `json:"api_key"`
}
