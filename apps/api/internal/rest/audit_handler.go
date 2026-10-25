package rest

import (
	"net/http"
	"strconv"

	"traffic-center/audit"
	"traffic-center/internal/httpx"
)

// AuditHandler exposes the read-only audit feed.
//
// The only verbs are admin-only reads: there is no endpoint that updates or
// deletes audit records, so tampering via the API is impossible by design.
type AuditHandler struct {
	usecase *audit.Usecase
}

// NewAuditHandler wires the audit handler.
func NewAuditHandler(usecase *audit.Usecase) *AuditHandler {
	return &AuditHandler{usecase: usecase}
}

// List handles GET /api/audit?limit=. Access is restricted to admins at the
// route level; non-admins receive 403 and never learn that the feed exists.
func (h *AuditHandler) List(w http.ResponseWriter, r *http.Request) {
	limit := 100
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 && parsed <= audit.MaxListSize {
			limit = parsed
		}
	}

	events, err := h.usecase.List(r.Context(), limit)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, events)
}
