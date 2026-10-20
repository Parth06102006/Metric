package rest

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"traffic-center/internal/httpx"
	"traffic-center/metrics"
	"traffic-center/project"
)

// MetricsHandler exposes the current snapshot.
type MetricsHandler struct {
	projects *project.Usecase
	metrics  *metrics.Usecase
}

// NewMetricsHandler wires the metrics handler.
func NewMetricsHandler(projects *project.Usecase, metrics *metrics.Usecase) *MetricsHandler {
	return &MetricsHandler{projects: projects, metrics: metrics}
}

// Get handles GET /api/projects/{projectID}/metrics.
func (h *MetricsHandler) Get(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectID")

	if _, err := h.projects.Get(r.Context(), projectID); err != nil {
		httpx.Error(w, r, err)
		return
	}

	snapshot, err := h.metrics.Current(r.Context(), projectID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	httpx.JSON(w, http.StatusOK, snapshot)
}
