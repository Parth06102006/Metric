package rest

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"traffic-center/domain"
	"traffic-center/ingest"
	"traffic-center/internal/httpx"
)

// IngestHandler accepts batches of request logs.
type IngestHandler struct {
	usecase *ingest.Usecase
}

// NewIngestHandler wires the ingest handler.
func NewIngestHandler(usecase *ingest.Usecase) *IngestHandler {
	return &IngestHandler{usecase: usecase}
}

type ingestRequest struct {
	Logs []domain.LogRecord `json:"logs"`
}

type ingestResponse struct {
	Accepted int    `json:"accepted,omitempty"`
	Error    string `json:"error,omitempty"`
}

// Ingest handles POST /ingest/{projectID}.
func (h *IngestHandler) Ingest(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		httpx.JSON(w, http.StatusUnsupportedMediaType, ingestResponse{Error: "content type must be application/json"})
		return
	}

	var request ingestRequest
	if err := httpx.DecodeJSON(w, r, &request); err != nil {
		httpx.Error(w, r, err)
		return
	}

	accepted, err := h.usecase.Ingest(
		r.Context(),
		chi.URLParam(r, "projectID"),
		r.Header.Get("X-API-Key"),
		request.Logs,
	)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	httpx.JSON(w, http.StatusAccepted, ingestResponse{Accepted: accepted})
}
