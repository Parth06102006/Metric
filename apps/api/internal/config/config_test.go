package config_test

import (
	"strings"
	"testing"

	"traffic-center/internal/config"
)

func TestLoadProductionRequiresProxySecret(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	t.Setenv("PROXY_SHARED_SECRET", "")

	_, err := config.Load()
	if err == nil {
		t.Fatal("expected error when PROXY_SHARED_SECRET is empty in production")
	}
	if !strings.Contains(err.Error(), "PROXY_SHARED_SECRET is required") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestLoadProductionWithProxySecret(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	t.Setenv("PROXY_SHARED_SECRET", "super-secret-token")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Env != "production" {
		t.Fatalf("Env = %q, want production", cfg.Env)
	}
	if cfg.AuthMode != "proxy" {
		t.Fatalf("AuthMode = %q, want proxy", cfg.AuthMode)
	}
	if cfg.ProxySecret != "super-secret-token" {
		t.Fatalf("ProxySecret = %q, want super-secret-token", cfg.ProxySecret)
	}
}

func TestLoadDevelopmentPreservesLocalBehavior(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("PROXY_SHARED_SECRET", "")
	t.Setenv("AUTH_MODE", "")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected error in development mode: %v", err)
	}
	if cfg.AuthMode != "dev" {
		t.Fatalf("AuthMode = %q, want dev", cfg.AuthMode)
	}
	if cfg.ProxySecret != "" {
		t.Fatalf("ProxySecret = %q, want empty", cfg.ProxySecret)
	}
}

func TestLoadProductionRejectsDevAuthMode(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	t.Setenv("AUTH_MODE", "dev")
	t.Setenv("PROXY_SHARED_SECRET", "secret")

	_, err := config.Load()
	if err == nil {
		t.Fatal("expected error for AUTH_MODE=dev in production")
	}
	if !strings.Contains(err.Error(), "AUTH_MODE=dev is not allowed") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestConfigAdminEmailsCaseInsensitive(t *testing.T) {
	t.Setenv("ADMIN_EMAILS", "Admin@Example.COM, Ops@Domain.Org")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if _, ok := cfg.AdminEmails["admin@example.com"]; !ok {
		t.Fatal("expected admin@example.com to be lowercased in AdminEmails map")
	}
	if _, ok := cfg.AdminEmails["ops@domain.org"]; !ok {
		t.Fatal("expected ops@domain.org to be lowercased in AdminEmails map")
	}
}
