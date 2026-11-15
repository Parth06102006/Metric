package rest

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"traffic-center/anomaly"
	"traffic-center/domain"
	"traffic-center/internal/httpx"
	"traffic-center/project"
)

// AnomalyHandler exposes anomaly history.
type AnomalyHandler struct {
	projects  *project.Usecase
	anomalies *anomaly.Usecase
	audit     domain.AuditSink
}

// NewAnomalyHandler wires the anomaly handler.
func NewAnomalyHandler(projects *project.Usecase, anomalies *anomaly.Usecase, audit domain.AuditSink) *AnomalyHandler {
	return &AnomalyHandler{projects: projects, anomalies: anomalies, audit: audit}
}

// List handles GET /api/projects/{projectID}/anomalies?limit=.
func (h *AnomalyHandler) List(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectID")

	if _, err := h.projects.Get(r.Context(), projectID); err != nil {
		auditDenial(h.audit, r, err)
		httpx.Error(w, r, err)
		return
	}

	limit := 100
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 && parsed <= 1000 {
			limit = parsed
		}
	}

	anomalies, err := h.anomalies.List(r.Context(), projectID, limit)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	httpx.JSON(w, http.StatusOK, anomalies)
}
