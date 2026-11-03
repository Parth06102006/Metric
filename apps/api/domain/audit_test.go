package domain_test

import (
	"testing"

	"traffic-center/domain"
)

func TestRedactDetails(t *testing.T) {
	redacted := domain.RedactDetails(map[string]string{
		"api_key":        "sk-live-abc",
		"auth_token":     "tok",
		"password":       "hunter2",
		"SESSION_COOKIE": "c",
		"Authorization":  "Bearer x",
		"proxy_secret":   "s",
		"credentials":    "c",
		"project_name":   "Checkout API",
		"path":           "/api/projects/p1/api-key",
	})

	for _, sensitive := range []string{"api_key", "auth_token", "password", "SESSION_COOKIE", "Authorization", "proxy_secret", "credentials"} {
		if got := redacted[sensitive]; got != "[redacted]" {
			t.Fatalf("detail %q = %q, want [redacted]", sensitive, got)
		}
	}
	if redacted["project_name"] != "Checkout API" || redacted["path"] != "/api/projects/p1/api-key" {
		t.Fatalf("benign details must be preserved: %+v", redacted)
	}

	empty := domain.RedactDetails(nil)
	if empty == nil || len(empty) != 0 {
		t.Fatalf("nil details = %#v, want empty non-nil map", empty)
	}
}
