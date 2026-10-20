package httpx

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"traffic-center/domain"
	"traffic-center/internal/reqid"
)

// MaxRequestBody caps decoded request bodies at 1 MiB.
const MaxRequestBody = 1 << 20

// ErrorResponse is the single error shape returned by the API.
type ErrorResponse struct {
	Error     string `json:"error"`
	RequestID string `json:"request_id,omitempty"`
}

// JSON writes a JSON response.
func JSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if status == http.StatusNoContent || value == nil {
		return
	}
	_ = json.NewEncoder(w).Encode(value)
}

// Error maps a domain error to an HTTP status in one place.
func Error(w http.ResponseWriter, r *http.Request, err error) {
	status := http.StatusInternalServerError
	message := "internal server error"

	switch {
	case errors.Is(err, domain.ErrNotFound):
		status, message = http.StatusNotFound, "not found"
	case errors.Is(err, domain.ErrValidation):
		status = http.StatusBadRequest
		message = strings.TrimPrefix(err.Error(), domain.ErrValidation.Error()+": ")
	case errors.Is(err, domain.ErrUnauthorized):
		status, message = http.StatusUnauthorized, "unauthorized"
	case errors.Is(err, domain.ErrForbidden):
		status, message = http.StatusForbidden, "forbidden"
	case errors.Is(err, domain.ErrConflict):
		status, message = http.StatusConflict, "conflict"
	}

	JSON(w, status, ErrorResponse{Error: message, RequestID: reqid.From(r.Context())})
}

// DecodeJSON enforces JSON content type, a body limit, and exactly one object.
func DecodeJSON(w http.ResponseWriter, r *http.Request, destination any) error {
	if !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		return domain.Validationf("content type must be application/json")
	}

	r.Body = http.MaxBytesReader(w, r.Body, MaxRequestBody)
	defer r.Body.Close()

	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(destination); err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			return domain.Validationf("request body exceeds %d bytes", MaxRequestBody)
		}
		return domain.Validationf("invalid JSON request")
	}

	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return domain.Validationf("request must contain one JSON object")
	}

	return nil
}
