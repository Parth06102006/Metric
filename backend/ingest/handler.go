package ingest

import (
	"crypto/subtle"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"traffic-center/backend/metrics"
)

const maxBatchSize = 1000
const maxBodySize = 1 << 20

type ProjectTarget struct {
	APIKey string
	Buffer *metrics.Buffer
}

type ProjectAuthenticator interface {
	AuthenticateProject(projectID, key string) (bool, error)
}

type Handler struct {
	Projects      map[string]ProjectTarget
	Authenticator ProjectAuthenticator
	Buffers       map[string]*metrics.Buffer
	mu            sync.Mutex
}

type ingestRequest struct {
	Logs []metrics.LogRecord `json:"logs"`
}

type ingestResponse struct {
	Accepted int    `json:"accepted,omitempty"`
	Error    string `json:"error,omitempty"`
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, ingestResponse{Error: "method not allowed"})
		return
	}

	const prefix = "/ingest/"
	if !strings.HasPrefix(r.URL.Path, prefix) {
		writeJSON(w, http.StatusNotFound, ingestResponse{Error: "route not found"})
		return
	}

	projectID := strings.TrimPrefix(r.URL.Path, prefix)
	if projectID == "" || strings.Contains(projectID, "/") {
		writeJSON(w, http.StatusNotFound, ingestResponse{Error: "invalid project ID"})
		return
	}

	buffer, ok := h.authenticateAndGetBuffer(projectID, r.Header.Get("X-API-Key"))
	if !ok || buffer == nil {
		writeJSON(w, http.StatusUnauthorized, ingestResponse{Error: "invalid project ID or API key"})
		return
	}

	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		writeJSON(w, http.StatusUnsupportedMediaType, ingestResponse{Error: "content type must be application/json"})
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxBodySize)
	defer r.Body.Close()

	decoder := json.NewDecoder(r.Body)
	var request ingestRequest

	if err := decoder.Decode(&request); err != nil {
		writeJSON(w, http.StatusBadRequest, ingestResponse{Error: "invalid JSON request"})
		return
	}

	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeJSON(w, http.StatusBadRequest, ingestResponse{Error: "request must contain one JSON object"})
		return
	}

	if len(request.Logs) == 0 || len(request.Logs) > maxBatchSize {
		writeJSON(w, http.StatusBadRequest, ingestResponse{Error: "logs must contain between 1 and 1000 records"})
		return
	}

	for _, record := range request.Logs {
		if record.Ts.IsZero() ||
			strings.TrimSpace(record.Endpoint) == "" ||
			strings.TrimSpace(record.Method) == "" ||
			record.Status < 100 || record.Status > 599 ||
			record.LatencyMs < 0 ||
			record.PayloadBytes < 0 ||
			net.ParseIP(record.IP) == nil {
			writeJSON(w, http.StatusBadRequest, ingestResponse{Error: "one or more log records are invalid"})
			return
		}
	}

	for _, record := range request.Logs {
		buffer.Add(record)
	}

	writeJSON(w, http.StatusAccepted, ingestResponse{Accepted: len(request.Logs)})
}

func (h *Handler) authenticateAndGetBuffer(projectID, key string) (*metrics.Buffer, bool) {
	if target, ok := h.Projects[projectID]; ok {
		if subtle.ConstantTimeCompare([]byte(target.APIKey), []byte(key)) != 1 {
			return nil, false
		}
		return target.Buffer, target.Buffer != nil
	}

	if h.Authenticator == nil || key == "" {
		return nil, false
	}

	valid, err := h.Authenticator.AuthenticateProject(projectID, key)
	if err != nil || !valid {
		return nil, false
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	if h.Buffers == nil {
		h.Buffers = make(map[string]*metrics.Buffer)
	}

	buffer, ok := h.Buffers[projectID]
	if !ok {
		buffer = metrics.NewBuffer(60 * time.Second)
		h.Buffers[projectID] = buffer
	}

	return buffer, true
}

func (h *Handler) GetBuffer(projectID string) (*metrics.Buffer, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()

	buffer, ok := h.Buffers[projectID]
	return buffer, ok
}

// ActiveProjects returns a slice of all project IDs that currently have buffers or targets.
func (h *Handler) ActiveProjects() []string {
	h.mu.Lock()
	defer h.mu.Unlock()

	seen := make(map[string]bool)
	var ids []string

	for id := range h.Projects {
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	for id := range h.Buffers {
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}

	return ids
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
