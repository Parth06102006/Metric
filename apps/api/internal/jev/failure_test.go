package jev

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestDiagnoseRejectsProviderFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{{"unauthorized", 401, `{}`}, {"payment required", 402, `{}`}, {"rate limited", 429, `{}`}, {"invalid json", 200, `{`}, {"missing answers", 200, `{"model":"x"}`}} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			c := New("test", "jev")
			c.baseURL = srv.URL
			if _, err := c.Diagnose(context.Background(), State{}); err == nil {
				t.Fatal("expected provider error")
			}
		})
	}
}
func TestDiagnoseHonorsCancellation(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-release }))
	defer srv.Close()
	defer close(release)
	c := New("test", "jev")
	c.baseURL = srv.URL
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := c.Diagnose(ctx, State{}); err == nil {
		t.Fatal("expected timeout")
	}
}
