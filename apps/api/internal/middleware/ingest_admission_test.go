package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestIngestAdmissionRejectsConcurrentRequests(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	h := IngestAdmission(1, time.Second)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
		w.WriteHeader(http.StatusAccepted)
	}))
	go func() { defer close(done); h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/", nil)) }()
	<-entered
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/", nil))
	if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") == "" {
		t.Fatalf("overload response %d", w.Code)
	}
	close(release)
	<-done
}
func TestIngestAdmissionHoldsSlotUntilHandlerReturns(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	h := IngestAdmission(1, 10*time.Millisecond)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(entered); <-r.Context().Done(); <-release }))
	go func() { defer close(done); h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/", nil)) }()
	<-entered
	time.Sleep(20 * time.Millisecond)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/", nil))
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("timed out handler released slot early: %d", w.Code)
	}
	close(release)
	<-done
}
