package rest

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"traffic-center/internal/httpx"
	"traffic-center/project"
)

// ProjectHandler exposes project routes.
type ProjectHandler struct {
	usecase *project.Usecase
}

// NewProjectHandler wires the project handler.
func NewProjectHandler(usecase *project.Usecase) *ProjectHandler {
	return &ProjectHandler{usecase: usecase}
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
