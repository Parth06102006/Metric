package middleware_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"traffic-center/domain"
	"traffic-center/internal/middleware"
)

// recordingSink captures audit events emitted by the middleware.
type recordingSink struct {
	events []domain.AuditEvent
}

func (s *recordingSink) Record(_ context.Context, event domain.AuditEvent) error {
	s.events = append(s.events, event)
	return nil
}

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

func TestIdentitySpoofedHeadersAreRejectedAndAudited(t *testing.T) {
	sink := &recordingSink{}
	build := func() http.Handler {
		return middleware.Identity(middleware.IdentityConfig{
			Mode:        "proxy",
			EmailHeader: "X-Auth-Request-Email",
			ProxySecret: "edge-secret",
			Audit:       sink,
		})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
	}

	// A client presenting identity headers but an untrusted secret is rejected
	// no matter whose identity it claims.
	spoof := httptest.NewRequest(http.MethodGet, "/", nil)
	spoof.Header.Set("X-Auth-Request-Email", "admin@example.com")
	spoof.Header.Set("X-Proxy-Secret", "attacker-guess")
	recorder := httptest.NewRecorder()
	build().ServeHTTP(recorder, spoof)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("spoofed request status = %d, want 401", recorder.Code)
	}

	// The denial is audit-logged, but the claimed identity is not treated as
	// the actor and the presented secret is never stored.
	if len(sink.events) != 1 {
		t.Fatalf("audit events = %d, want 1: %+v", len(sink.events), sink.events)
	}
	event := sink.events[0]
	if event.Action != domain.AuditAuthDenied || event.Outcome != domain.AuditOutcomeFailure {
		t.Fatalf("event = %+v, want auth.denied with failure outcome", event)
	}
	if event.Details["reason"] != "proxy_secret_mismatch" {
		t.Fatalf("reason = %q, want proxy_secret_mismatch", event.Details["reason"])
	}
	if event.ActorEmail != "" {
		t.Fatalf("actor = %q, want empty: a spoofed identity is not evidence", event.ActorEmail)
	}
	if _, claimed := event.Details["presented_secret"]; claimed {
		t.Fatal("audit event must not contain the presented secret")
	}

	// The trusted edge secret authenticates the caller and logs nothing.
	var seen domain.Identity
	valid := httptest.NewRequest(http.MethodGet, "/", nil)
	valid.Header.Set("X-Auth-Request-Email", "ops@example.com")
	valid.Header.Set("X-Proxy-Secret", "edge-secret")
	recorder = httptest.NewRecorder()
	// The same sink instance: the valid call must not append a second event.
	handler := middleware.Identity(middleware.IdentityConfig{
		Mode:        "proxy",
		EmailHeader: "X-Auth-Request-Email",
		ProxySecret: "edge-secret",
		Audit:       sink,
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen, _ = domain.IdentityFrom(r.Context())
		w.WriteHeader(http.StatusOK)
	}))
	handler.ServeHTTP(recorder, valid)
	if recorder.Code != http.StatusOK {
		t.Fatalf("valid request status = %d, want 200", recorder.Code)
	}
	if seen.Email != "ops@example.com" {
		t.Fatalf("identity = %+v, want ops@example.com", seen)
	}
	if len(sink.events) != 1 {
		t.Fatalf("valid request must not be audit-logged, events = %d", len(sink.events))
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

func TestIdentityProxyEmptyAdminListDeniesAdmin(t *testing.T) {
	var seen domain.Identity
	identity := middleware.Identity(middleware.IdentityConfig{
		Mode:        "proxy",
		EmailHeader: "X-Auth-Request-Email",
		AdminEmails: map[string]struct{}{}, // Empty admin list in production/proxy mode
	})

	handler := identity(middleware.RequireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen, _ = domain.IdentityFrom(r.Context())
		w.WriteHeader(http.StatusOK)
	})))

	request := httptest.NewRequest(http.MethodPost, "/", nil)
	request.Header.Set("X-Auth-Request-Email", "user@example.com")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 Forbidden when admin list is empty", recorder.Code)
	}
	if seen.Admin {
		t.Fatalf("Admin = true, want false when admin list is empty")
	}
}

func TestIdentityAdminMatchingIsCaseInsensitive(t *testing.T) {
	var seen domain.Identity
	identity := middleware.Identity(middleware.IdentityConfig{
		Mode:        "proxy",
		EmailHeader: "X-Auth-Request-Email",
		AdminEmails: map[string]struct{}{"admin@example.com": {}},
	})

	handler := identity(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen, _ = domain.IdentityFrom(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("X-Auth-Request-Email", "AdMiN@ExAmPlE.CoM")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	if !seen.Admin {
		t.Fatalf("Admin = false, want true for case-insensitive match")
	}
}
