package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"traffic-center/backend/metrics"
	"traffic-center/backend/store"
)

const maxRequestBody = 1 << 20

type BufferProvider interface {
	GetBuffer(projectID string) (*metrics.Buffer, bool)
}

type Handler struct {
	Store   *store.Store
	Buffers BufferProvider
}

type createProjectRequest struct {
	Name string `json:"name"`
}

type createProjectResponse struct {
	ProjectID string `json:"project_id"`
	Name      string `json:"name"`
	APIKey    string `json:"api_key"`
}

type errorResponse struct {
	Error string `json:"error"`
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.Store == nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "store unavailable"})
		return
	}

	path := strings.TrimSuffix(r.URL.Path, "/")

	if path == "/api/projects" {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "method not allowed"})
			return
		}
		h.createProject(w, r)
		return
	}

	const prefix = "/api/projects/"
	if !strings.HasPrefix(path, prefix) {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "route not found"})
		return
	}

	parts := strings.Split(strings.TrimPrefix(path, prefix), "/")
	if len(parts) == 0 || parts[0] == "" {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "project ID required"})
		return
	}

	projectID := parts[0]

	if len(parts) == 1 {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "method not allowed"})
			return
		}
		h.getProject(w, projectID)
		return
	}

	if len(parts) == 2 {
		switch parts[1] {
		case "thresholds":
			switch r.Method {
			case http.MethodGet:
				h.getThresholds(w, projectID)
			case http.MethodPut:
				h.updateThresholds(w, r, projectID)
			default:
				w.Header().Set("Allow", "GET, PUT")
				writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "method not allowed"})
			}
			return

		case "metrics":
			if r.Method != http.MethodGet {
				w.Header().Set("Allow", http.MethodGet)
				writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "method not allowed"})
				return
			}
			h.getMetrics(w, projectID)
			return
		}
	}

	writeJSON(w, http.StatusNotFound, errorResponse{Error: "route not found"})
}

func (h *Handler) createProject(w http.ResponseWriter, r *http.Request) {
	var request createProjectRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid JSON request"})
		return
	}

	project, apiKey, err := h.Store.CreateProject(request.Name)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}

	writeJSON(w, http.StatusCreated, createProjectResponse{
		ProjectID: project.ID,
		Name:      project.Name,
		APIKey:    apiKey,
	})
}

func (h *Handler) getProject(w http.ResponseWriter, projectID string) {
	project, err := h.Store.GetProject(projectID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, errorResponse{Error: "project not found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "could not retrieve project"})
		return
	}

	writeJSON(w, http.StatusOK, project)
}

func (h *Handler) getThresholds(w http.ResponseWriter, projectID string) {
	thresholds, err := h.Store.GetThresholds(projectID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, errorResponse{Error: "project not found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "could not retrieve thresholds"})
		return
	}

	writeJSON(w, http.StatusOK, thresholds)
}

func (h *Handler) updateThresholds(w http.ResponseWriter, r *http.Request, projectID string) {
	var thresholds store.Thresholds
	if err := decodeJSON(w, r, &thresholds); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid JSON request"})
		return
	}

	if err := h.Store.UpdateThresholds(projectID, thresholds); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, errorResponse{Error: "project not found"})
			return
		}
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, thresholds)
}

func (h *Handler) getMetrics(w http.ResponseWriter, projectID string) {
	if _, err := h.Store.GetProject(projectID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, errorResponse{Error: "project not found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "could not retrieve project"})
		return
	}

	if h.Buffers == nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "metrics unavailable"})
		return
	}

	buffer, ok := h.Buffers.GetBuffer(projectID)
	if !ok || buffer == nil {
		writeJSON(w, http.StatusOK, metrics.Compute(nil, 60))
		return
	}

	records := buffer.Window(time.Now())
	writeJSON(w, http.StatusOK, metrics.Compute(records, 60))
}

func decodeJSON(w http.ResponseWriter, r *http.Request, destination any) error {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		return errors.New("content type must be application/json")
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
	defer r.Body.Close()

	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(destination); err != nil {
		return err
	}

	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return errors.New("request must contain one JSON object")
	}

	return nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
