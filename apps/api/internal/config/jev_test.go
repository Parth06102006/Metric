package config_test

import (
	"testing"
	"traffic-center/internal/config"
)

func TestJevConfiguration(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "test-value")
	t.Setenv("TYPESAFE_API_KEY", "legacy-key-not-for-openrouter")
	t.Setenv("JEV_MODEL", "jev-test")
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.JevAPIKey != "test-value" || cfg.JevModel != "jev-test" {
		t.Fatalf("OpenRouter configuration not loaded")
	}
}

func TestJevDefaultsAndDoesNotUseTypeSafeKey(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "")
	t.Setenv("TYPESAFE_API_KEY", "direct-typesafe-key")
	t.Setenv("JEV_MODEL", "")
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.JevAPIKey != "" || cfg.JevModel != "typesafe/jev-1.14" {
		t.Fatalf("unexpected OpenRouter defaults")
	}
}
