package rest

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"traffic-center/domain"
	"traffic-center/internal/httpx"
	"traffic-center/project"
)

// ProjectHandler exposes project routes.
type ProjectHandler struct {
	usecase   *project.Usecase
	lifecycle *project.Lifecycle
	audit     domain.AuditSink
}

// NewProjectHandler wires the project handler.
func NewProjectHandler(usecase *project.Usecase, lifecycle *project.Lifecycle, audit domain.AuditSink) *ProjectHandler {
	return &ProjectHandler{usecase: usecase, lifecycle: lifecycle, audit: audit}
}

type createProjectRequest struct {
	Name string `json:"name"`
}

type assignOwnerRequest struct {
	OwnerEmail string `json:"owner_email"`
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
	logAudit(h.audit, r.Context(), actor(r), domain.AuditProjectCreate, created.ID,
		domain.AuditOutcomeSuccess, map[string]string{"project_name": created.Name})

	httpx.JSON(w, http.StatusCreated, created)
}

// Get handles GET /api/projects/{projectID}.
func (h *ProjectHandler) Get(w http.ResponseWriter, r *http.Request) {
	project, err := h.usecase.Get(r.Context(), chi.URLParam(r, "projectID"))
	if err != nil {
		auditDenial(h.audit, r, err)
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

// AssignOwner handles PATCH /api/projects/{projectID}. Admins use it to hand a
// project to the person who should see it, and to claim projects created
// before ownership existed. An empty owner email makes the project admin-only.
func (h *ProjectHandler) AssignOwner(w http.ResponseWriter, r *http.Request) {
	var request assignOwnerRequest
	if err := httpx.DecodeJSON(w, r, &request); err != nil {
		httpx.Error(w, r, err)
		return
	}

	updated, err := h.lifecycle.AssignOwner(r.Context(), chi.URLParam(r, "projectID"), request.OwnerEmail)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	logAudit(h.audit, r.Context(), actor(r), domain.AuditProjectOwnerAssign, updated.ID,
		domain.AuditOutcomeSuccess, map[string]string{"owner_email": updated.OwnerEmail})

	httpx.JSON(w, http.StatusOK, updated)
}

type viewerRequest struct {
	Email string `json:"email"`
}

// KnownUsers exposes the authenticated directory to administrators only.
func (h *ProjectHandler) KnownUsers(w http.ResponseWriter, r *http.Request) {
	users, err := h.usecase.KnownUsers(r.Context())
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httpx.JSON(w, http.StatusOK, users)
}

func (h *ProjectHandler) Viewers(w http.ResponseWriter, r *http.Request) {
	users, err := h.usecase.Viewers(r.Context(), chi.URLParam(r, "projectID"))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httpx.JSON(w, http.StatusOK, users)
}

func (h *ProjectHandler) AddViewer(w http.ResponseWriter, r *http.Request) {
	var request viewerRequest
	if err := httpx.DecodeJSON(w, r, &request); err != nil {
		httpx.Error(w, r, err)
		return
	}
	projectID := chi.URLParam(r, "projectID")
	if err := h.lifecycle.AddViewer(r.Context(), projectID, request.Email); err != nil {
		httpx.Error(w, r, err)
		return
	}
	logAudit(h.audit, r.Context(), actor(r), domain.AuditProjectViewerAdd, projectID, domain.AuditOutcomeSuccess, map[string]string{"viewer_email": domain.SanitizeActor(request.Email)})
	w.WriteHeader(http.StatusNoContent)
}

func (h *ProjectHandler) RemoveViewer(w http.ResponseWriter, r *http.Request) {
	var request viewerRequest
	if err := httpx.DecodeJSON(w, r, &request); err != nil {
		httpx.Error(w, r, err)
		return
	}
	projectID := chi.URLParam(r, "projectID")
	if err := h.lifecycle.RemoveViewer(r.Context(), projectID, request.Email); err != nil {
		httpx.Error(w, r, err)
		return
	}
	logAudit(h.audit, r.Context(), actor(r), domain.AuditProjectViewerRemove, projectID, domain.AuditOutcomeSuccess, map[string]string{"viewer_email": domain.SanitizeActor(request.Email)})
	w.WriteHeader(http.StatusNoContent)
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
	logAudit(h.audit, r.Context(), actor(r), domain.AuditKeyRotate, rotated.ID,
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

	logAudit(h.audit, r.Context(), actor(r), domain.AuditKeyRevoke, projectID,
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

	logAudit(h.audit, r.Context(), actor(r), domain.AuditProjectDelete, projectID,
		domain.AuditOutcomeSuccess, nil)

	w.WriteHeader(http.StatusNoContent)
}
