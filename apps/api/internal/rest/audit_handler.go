package rest

import (
	"net/http"
	"strconv"
	"time"

	"traffic-center/audit"
	"traffic-center/domain"
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

// List handles GET /api/audit?limit=&action=&project_id=&before=. Access is
// restricted to admins at the route level; non-admins receive 403 and never
// learn that the feed exists.
func (h *AuditHandler) List(w http.ResponseWriter, r *http.Request) {
	query := domain.AuditQuery{
		Limit:     100,
		Action:    r.URL.Query().Get("action"),
		ProjectID: r.URL.Query().Get("project_id"),
	}

	if raw := r.URL.Query().Get("limit"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 && parsed <= audit.MaxListSize {
			query.Limit = parsed
		}
	}

	// "before" and "before_id" are an exclusive cursor: pass the timestamp and
	// id of the oldest event you already have to page further back.
	if raw := r.URL.Query().Get("before"); raw != "" {
		parsed, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			httpx.Error(w, r, domain.Validationf("before must be an RFC3339 timestamp"))
			return
		}
		query.Before = parsed
	}
	if raw := r.URL.Query().Get("before_id"); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed < 0 {
			httpx.Error(w, r, domain.Validationf("before_id must be a non-negative integer"))
			return
		}
		query.BeforeID = parsed
	}

	events, err := h.usecase.List(r.Context(), query)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, events)
}
