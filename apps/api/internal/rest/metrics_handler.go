package rest

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"traffic-center/domain"
	"traffic-center/internal/httpx"
	"traffic-center/metrics"
	"traffic-center/project"
)

// MetricsHandler exposes current and recent persisted snapshots.
type MetricsHandler struct {
	projects *project.Usecase
	metrics  *metrics.Usecase
	audit    domain.AuditSink
}

// NewMetricsHandler wires the metrics handler.
func NewMetricsHandler(projects *project.Usecase, metrics *metrics.Usecase, audit domain.AuditSink) *MetricsHandler {
	return &MetricsHandler{projects: projects, metrics: metrics, audit: audit}
}

// Get handles GET /api/projects/{projectID}/metrics.
func (h *MetricsHandler) Get(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectID")

	if _, err := h.projects.Get(r.Context(), projectID); err != nil {
		auditDenial(h.audit, r, err)
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

// History handles GET /api/projects/{projectID}/metrics/history.
func (h *MetricsHandler) History(w http.ResponseWriter, r *http.Request) {
	// Project history must not be retained by browser or intermediary caches.
	w.Header().Set("Cache-Control", "no-store")
	projectID := chi.URLParam(r, "projectID")
	if _, err := h.projects.Get(r.Context(), projectID); err != nil {
		auditDenial(h.audit, r, err)
		httpx.Error(w, r, err)
		return
	}
	snapshots, err := h.metrics.RecentHistory(r.Context(), projectID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, snapshots)
}
