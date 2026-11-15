package httpx

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"traffic-center/domain"
)

func TestStorageErrorIsRetryableWithoutLeakingDetails(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/ingest/p", nil)
	Error(w, r, fmt.Errorf("%w: private filesystem path", domain.ErrStorageUnavailable))
	if w.Code != http.StatusServiceUnavailable || w.Header().Get("Retry-After") == "" || w.Body.String() == "" {
		t.Fatalf("status=%d headers=%v body=%s", w.Code, w.Header(), w.Body.String())
	}
	if w.Body.String() != "{\"error\":\"storage unavailable; retry later\"}\n" {
		t.Fatalf("details leaked: %s", w.Body.String())
	}
}
