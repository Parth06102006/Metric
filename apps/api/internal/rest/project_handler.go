package rest

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"traffic-center/audit"
	"traffic-center/domain"
	"traffic-center/internal/httpx"
	"traffic-center/project"
)

// ProjectHandler exposes project routes.
type ProjectHandler struct {
	usecase   *project.Usecase
	lifecycle *project.Lifecycle
	audit     *audit.Usecase
}

// NewProjectHandler wires the project handler.
func NewProjectHandler(usecase *project.Usecase, lifecycle *project.Lifecycle, events *audit.Usecase) *ProjectHandler {
	return &ProjectHandler{usecase: usecase, lifecycle: lifecycle, audit: events}
}

// actor returns the caller email from the trusted identity context.
func actor(r *http.Request) string {
	identity, ok := domain.IdentityFrom(r.Context())
	if !ok {
		return ""
	}
	return domain.SanitizeActor(identity.Email)
}

type createProjectRequest struct {
	Name string `json:"name"`
}

// Create handles POST /api/projects.
func (h *ProjectHandler) Create(w http.ResponseWriter, r *http.Request) {
	var request createProjectRequest
	if err := httpx.DecodeJSON(w, r, &request); err != nil {
		httpx.Error(w, r, err)
		return
	}

	created, err := h.usecase.Create(r.Context(), request.Name)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	// The audit trail records the action, never the minted API key.
	h.audit.Log(r.Context(), actor(r), domain.AuditProjectCreate, created.ID,
		domain.AuditOutcomeSuccess, map[string]string{"project_name": created.Name})

	httpx.JSON(w, http.StatusCreated, created)
}

// Get handles GET /api/projects/{projectID}.
func (h *ProjectHandler) Get(w http.ResponseWriter, r *http.Request) {
	project, err := h.usecase.Get(r.Context(), chi.URLParam(r, "projectID"))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, project)
}

// List handles GET /api/projects.
func (h *ProjectHandler) List(w http.ResponseWriter, r *http.Request) {
	projects, err := h.usecase.List(r.Context())
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, projects)
}

// RotateKey handles POST /api/projects/{projectID}/api-key. The new plaintext
// key is returned exactly once; the previous key stops working immediately.
func (h *ProjectHandler) RotateKey(w http.ResponseWriter, r *http.Request) {
	rotated, err := h.lifecycle.RotateKey(r.Context(), chi.URLParam(r, "projectID"))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	// No detail is attached: plaintext keys never enter the audit trail.
	h.audit.Log(r.Context(), actor(r), domain.AuditKeyRotate, rotated.ID,
		domain.AuditOutcomeSuccess, nil)

	httpx.JSON(w, http.StatusOK, rotated)
}

// RevokeKey handles DELETE /api/projects/{projectID}/api-key. Ingest traffic
// is rejected until a new key is rotated.
func (h *ProjectHandler) RevokeKey(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectID")
	if err := h.lifecycle.RevokeKey(r.Context(), projectID); err != nil {
		httpx.Error(w, r, err)
		return
	}

	h.audit.Log(r.Context(), actor(r), domain.AuditKeyRevoke, projectID,
		domain.AuditOutcomeSuccess, nil)

	w.WriteHeader(http.StatusNoContent)
}

// Delete handles DELETE /api/projects/{projectID}. The project, its data, and
// its live streams are removed; ingest traffic and new subscriptions are
// rejected afterwards.
func (h *ProjectHandler) Delete(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectID")
	if err := h.lifecycle.DeleteProject(r.Context(), projectID); err != nil {
		httpx.Error(w, r, err)
		return
	}

	h.audit.Log(r.Context(), actor(r), domain.AuditProjectDelete, projectID,
		domain.AuditOutcomeSuccess, nil)

	w.WriteHeader(http.StatusNoContent)
}
