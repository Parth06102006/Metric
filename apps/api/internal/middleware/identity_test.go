package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"traffic-center/domain"
	"traffic-center/internal/middleware"
)

func TestIdentityDevInjectsCaller(t *testing.T) {
	var seen domain.Identity

	handler := middleware.Identity(middleware.IdentityConfig{
		Mode:     "dev",
		DevEmail: "dev@example.com",
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen, _ = domain.IdentityFrom(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	if seen.Email != "dev@example.com" || !seen.Admin {
		t.Fatalf("identity = %+v", seen)
	}
}

func TestIdentityProxyRequiresHeader(t *testing.T) {
	handler := middleware.Identity(middleware.IdentityConfig{
		Mode:        "proxy",
		EmailHeader: "X-Auth-Request-Email",
		UserHeader:  "X-Auth-Request-User",
		AdminEmails: map[string]struct{}{},
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", recorder.Code)
	}

	var seen domain.Identity
	handler = middleware.Identity(middleware.IdentityConfig{
		Mode:        "proxy",
		EmailHeader: "X-Auth-Request-Email",
		UserHeader:  "X-Auth-Request-User",
		AdminEmails: map[string]struct{}{"ops@example.com": {}},
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen, _ = domain.IdentityFrom(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("X-Auth-Request-Email", "ops@example.com")
	request.Header.Set("X-Auth-Request-User", "ops")

	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	if seen.Email != "ops@example.com" || !seen.Admin {
		t.Fatalf("identity = %+v", seen)
	}
}

func TestIdentityRejectsBadProxySecret(t *testing.T) {
	handler := middleware.Identity(middleware.IdentityConfig{
		Mode:        "proxy",
		EmailHeader: "X-Auth-Request-Email",
		ProxySecret: "super-secret",
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("X-Auth-Request-Email", "ops@example.com")
	request.Header.Set("X-Proxy-Secret", "wrong")

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", recorder.Code)
	}
}

func TestRequireAdmin(t *testing.T) {
	identity := middleware.Identity(middleware.IdentityConfig{
		Mode:        "proxy",
		EmailHeader: "X-Auth-Request-Email",
		AdminEmails: map[string]struct{}{"admin@example.com": {}},
	})

	handler := identity(middleware.RequireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})))

	adminRequest := httptest.NewRequest(http.MethodPost, "/", nil)
	adminRequest.Header.Set("X-Auth-Request-Email", "admin@example.com")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, adminRequest)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("admin status = %d, want 204", recorder.Code)
	}

	viewerRequest := httptest.NewRequest(http.MethodPost, "/", nil)
	viewerRequest.Header.Set("X-Auth-Request-Email", "viewer@example.com")
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, viewerRequest)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("viewer status = %d, want 403", recorder.Code)
	}
}
