package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is the fully resolved process configuration.
type Config struct {
	Addr         string
	DatabasePath string

	Env      string
	LogLevel string

	AuthMode        string
	AuthEmailHeader string
	AuthUserHeader  string
	ProxySecret     string
	DevEmail        string
	AdminEmails     map[string]struct{}
	TrustProxy      bool
	AllowedOrigins  map[string]struct{}

	MetricsWindow     time.Duration
	WorkerInterval    time.Duration
	DedupeWindow      time.Duration
	SnapshotRetention time.Duration
	AuditRetention    time.Duration
	APITimeout        time.Duration
	ReadHeaderTimeout time.Duration
	ShutdownTimeout   time.Duration

	MaxProjectRecords   int
	MaxProjectEndpoints int
	MaxTotalRecords     int
	MaxLiveProjects     int
	MaxProjectBytes     int64
	MaxTotalBytes       int64
	IngestConcurrency   int
	SQLiteMaxBytes      int64
	MinFreeDiskBytes    int64

	GeminiAPIKey        string
	GeminiModel         string
	GeminiMaxDailyCalls int
}

// Load reads configuration from the environment and applies safe defaults.
func Load() (Config, error) {
	cfg := Config{
		Addr:                env("ADDR", ":8080"),
		DatabasePath:        env("DATABASE_PATH", "traffic.db"),
		Env:                 env("APP_ENV", "development"),
		LogLevel:            env("LOG_LEVEL", "info"),
		AuthMode:            env("AUTH_MODE", ""),
		AuthEmailHeader:     env("AUTH_EMAIL_HEADER", "X-Auth-Request-Email"),
		AuthUserHeader:      env("AUTH_USER_HEADER", "X-Auth-Request-User"),
		ProxySecret:         os.Getenv("PROXY_SHARED_SECRET"),
		DevEmail:            env("DEV_USER_EMAIL", "dev@localhost"),
		AdminEmails:         csvSet(os.Getenv("ADMIN_EMAILS")),
		TrustProxy:          envBool("TRUST_PROXY", true),
		AllowedOrigins:      csvSet(os.Getenv("ALLOWED_ORIGINS")),
		MetricsWindow:       envDuration("METRICS_WINDOW", 60*time.Second),
		WorkerInterval:      envDuration("WORKER_INTERVAL", 5*time.Second),
		DedupeWindow:        envDuration("DEDUPE_WINDOW", 60*time.Second),
		SnapshotRetention:   envDuration("SNAPSHOT_RETENTION", 7*24*time.Hour),
		AuditRetention:      envDuration("AUDIT_RETENTION", 90*24*time.Hour),
		APITimeout:          envDuration("API_TIMEOUT", 15*time.Second),
		ReadHeaderTimeout:   envDuration("READ_HEADER_TIMEOUT", 5*time.Second),
		ShutdownTimeout:     envDuration("SHUTDOWN_TIMEOUT", 10*time.Second),
		GeminiAPIKey:        os.Getenv("GEMINI_API_KEY"),
		GeminiModel:         env("GEMINI_MODEL", "gemini-decision-1"),
		GeminiMaxDailyCalls: 100,
		MaxProjectRecords:   50000,
		MaxProjectEndpoints: 512,
		MaxTotalRecords:     200000,
		MaxLiveProjects:     512,
		MaxProjectBytes:     16 << 20,
		MaxTotalBytes:       64 << 20,
		IngestConcurrency:   16,
		SQLiteMaxBytes:      2 << 30,
		MinFreeDiskBytes:    256 << 20,
	}

	for key, target := range map[string]*int{
		"MAX_PROJECT_RECORDS": &cfg.MaxProjectRecords, "MAX_PROJECT_ENDPOINTS": &cfg.MaxProjectEndpoints, "MAX_TOTAL_RECORDS": &cfg.MaxTotalRecords,
		"MAX_LIVE_PROJECTS": &cfg.MaxLiveProjects, "INGEST_CONCURRENCY": &cfg.IngestConcurrency, "GEMINI_MAX_DAILY_CALLS": &cfg.GeminiMaxDailyCalls,
	} {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			v, err := strconv.Atoi(value)
			if err != nil || v <= 0 || v > 10000000 {
				return Config{}, fmt.Errorf("%s must be between 1 and 10000000", key)
			}
			*target = v
		}
	}
	for key, target := range map[string]*int64{"MAX_PROJECT_BYTES": &cfg.MaxProjectBytes, "MAX_TOTAL_BYTES": &cfg.MaxTotalBytes} {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			v, err := strconv.ParseInt(value, 10, 64)
			if err != nil || v <= 0 || v > 1<<40 {
				return Config{}, fmt.Errorf("%s must be between 1 and 1099511627776", key)
			}
			*target = v
		}
	}
	for key, target := range map[string]*int64{"SQLITE_MAX_BYTES": &cfg.SQLiteMaxBytes, "MIN_FREE_DISK_BYTES": &cfg.MinFreeDiskBytes} {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			v, err := strconv.ParseInt(value, 10, 64)
			if err != nil || v < 1<<20 || v > 1<<40 {
				return Config{}, fmt.Errorf("%s must be between 1048576 and 1099511627776", key)
			}
			*target = v
		}
	}
	if cfg.GeminiAPIKey != "" && cfg.GeminiModel != "gemini-decision-1" {
		return Config{}, fmt.Errorf("GEMINI_MODEL must be gemini-decision-1 for the Decisions API")
	}

	if port := os.Getenv("PORT"); port != "" {
		cfg.Addr = ":" + port
	}

	if cfg.AuthMode == "" {
		if cfg.Env == "production" {
			cfg.AuthMode = "proxy"
		} else {
			cfg.AuthMode = "dev"
		}
	}

	switch cfg.AuthMode {
	case "dev", "proxy":
	default:
		return Config{}, fmt.Errorf("AUTH_MODE must be dev or proxy, got %q", cfg.AuthMode)
	}

	if cfg.Env == "production" && cfg.AuthMode == "dev" {
		return Config{}, fmt.Errorf("AUTH_MODE=dev is not allowed when APP_ENV=production")
	}

	if cfg.Env == "production" && strings.TrimSpace(cfg.ProxySecret) == "" {
		return Config{}, fmt.Errorf("PROXY_SHARED_SECRET is required when APP_ENV=production")
	}

	return cfg, nil
}

// Production reports whether the process runs with production guards.
func (c Config) Production() bool {
	return c.Env == "production"
}

func env(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func envBool(key string, fallback bool) bool {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func envDuration(key string, fallback time.Duration) time.Duration {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

func csvSet(value string) map[string]struct{} {
	set := make(map[string]struct{})
	for _, item := range strings.Split(value, ",") {
		item = strings.ToLower(strings.TrimSpace(item))
		if item != "" {
			set[item] = struct{}{}
		}
	}
	return set
}
