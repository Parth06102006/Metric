package config_test

import (
	"testing"
	"traffic-center/internal/config"
)

func TestGeminiConfiguration(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("GEMINI_API_KEY", "test-value")
	t.Setenv("GEMINI_MODEL", "")
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GeminiAPIKey != "test-value" || cfg.GeminiModel != "gemini-decision-1" {
		t.Fatal("incorrect decisions configuration")
	}
	t.Setenv("GEMINI_MODEL", "gemini-2.0-flash")
	if _, err := config.Load(); err == nil {
		t.Fatal("unsupported decisions model accepted")
	}
}
func TestResourceLimitsRejectInvalidConfig(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("GEMINI_API_KEY", "")
	for _, name := range []string{"MAX_PROJECT_RECORDS", "MAX_PROJECT_ENDPOINTS", "MAX_TOTAL_RECORDS", "MAX_LIVE_PROJECTS", "MAX_PROJECT_BYTES", "MAX_TOTAL_BYTES", "INGEST_CONCURRENCY", "SQLITE_MAX_BYTES", "MIN_FREE_DISK_BYTES", "GEMINI_MAX_DAILY_CALLS"} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(name, "-1")
			if _, err := config.Load(); err == nil {
				t.Fatal("invalid limit accepted")
			}
		})
	}
}
