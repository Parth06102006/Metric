package rest

import (
	"net/http"
	"strings"

	"traffic-center/domain"
	"traffic-center/internal/httpx"
)

// IdentityHandler exposes the current caller.
type IdentityHandler struct{ projects domain.ProjectRepository }

// NewIdentityHandler creates the identity handler.
func NewIdentityHandler(projects domain.ProjectRepository) *IdentityHandler {
	return &IdentityHandler{projects: projects}
}

type meResponse struct {
	Email string `json:"email"`
	User  string `json:"user,omitempty"`
	Admin bool   `json:"admin"`
}

// Me handles GET /api/auth/me.
func (h *IdentityHandler) Me(w http.ResponseWriter, r *http.Request) {
	identity, ok := domain.IdentityFrom(r.Context())
	if !ok {
		httpx.Error(w, r, domain.ErrUnauthorized)
		return
	}
	// Only identities asserted by the trusted middleware enter the directory.
	email := strings.ToLower(strings.TrimSpace(identity.Email))
	if email == "" || domain.ValidateEmail(email) != nil {
		httpx.Error(w, r, domain.ErrUnauthorized)
		return
	}
	if err := h.projects.RecordUser(r.Context(), email); err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httpx.JSON(w, http.StatusOK, meResponse{
		Email: identity.Email,
		User:  identity.User,
		Admin: identity.Admin,
	})
}

// HealthHandler exposes liveness without authentication.
type HealthHandler struct{}

// NewHealthHandler creates the health handler.
func NewHealthHandler() *HealthHandler {
	return &HealthHandler{}
}

// Health handles GET /health.
func (h *HealthHandler) Health(w http.ResponseWriter, _ *http.Request) {
	httpx.JSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
