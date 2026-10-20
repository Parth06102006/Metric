package rest

import (
	"net/http"

	"traffic-center/domain"
	"traffic-center/internal/httpx"
)

// IdentityHandler exposes the current caller.
type IdentityHandler struct{}

// NewIdentityHandler creates the identity handler.
func NewIdentityHandler() *IdentityHandler {
	return &IdentityHandler{}
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
