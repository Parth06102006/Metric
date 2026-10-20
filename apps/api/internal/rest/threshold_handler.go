package rest

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"traffic-center/domain"
	"traffic-center/internal/httpx"
	"traffic-center/threshold"
)

// ThresholdHandler exposes threshold routes.
type ThresholdHandler struct {
	usecase *threshold.Usecase
}

// NewThresholdHandler wires the threshold handler.
func NewThresholdHandler(usecase *threshold.Usecase) *ThresholdHandler {
	return &ThresholdHandler{usecase: usecase}
}

// Get handles GET /api/projects/{projectID}/thresholds.
func (h *ThresholdHandler) Get(w http.ResponseWriter, r *http.Request) {
	thresholds, err := h.usecase.Get(r.Context(), chi.URLParam(r, "projectID"))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, thresholds)
}

// Update handles PUT /api/projects/{projectID}/thresholds.
func (h *ThresholdHandler) Update(w http.ResponseWriter, r *http.Request) {
	var thresholds domain.Threshold
	if err := httpx.DecodeJSON(w, r, &thresholds); err != nil {
		httpx.Error(w, r, err)
		return
	}

	if err := h.usecase.Update(r.Context(), chi.URLParam(r, "projectID"), thresholds); err != nil {
		httpx.Error(w, r, err)
		return
	}

	httpx.JSON(w, http.StatusOK, thresholds)
}
