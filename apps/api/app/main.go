package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"traffic-center/anomaly"
	anomalysqlite "traffic-center/anomaly/repository/sqlite"
	"traffic-center/audit"
	auditsqlite "traffic-center/audit/repository/sqlite"
	"traffic-center/ingest"
	"traffic-center/internal/config"
	"traffic-center/internal/llm"
	"traffic-center/internal/server"
	"traffic-center/internal/sqlite"
	"traffic-center/internal/ws"
	"traffic-center/metrics"
	metricsmemory "traffic-center/metrics/repository/memory"
	metricssqlite "traffic-center/metrics/repository/sqlite"
	"traffic-center/project"
	projectsqlite "traffic-center/project/repository/sqlite"
	"traffic-center/threshold"
	thresholdsqlite "traffic-center/threshold/repository/sqlite"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--healthcheck" {
		os.Exit(healthcheck())
	}

	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	logger := newLogger(cfg.LogLevel)

	if cfg.AuthMode == "dev" {
		logger.Warn("authentication is disabled (AUTH_MODE=dev)", "dev_email", cfg.DevEmail)
	}

	if cfg.Env == "production" && len(cfg.AdminEmails) == 0 {
		logger.Warn("ADMIN_EMAILS is empty in production; admin actions will be denied")
	}

	db, err := sqlite.Open(cfg.DatabasePath)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer db.Close()

	// Repositories (adapters).
	projectRepo := projectsqlite.New(db)
	thresholdRepo := thresholdsqlite.New(db)
	recordRepo := metricsmemory.New(cfg.MetricsWindow)
	snapshotRepo := metricssqlite.New(db)
	anomalyRepo := anomalysqlite.New(db)
	auditRepo := auditsqlite.New(db)
	hub := ws.NewHub()

	// Usecases.
	projectUsecase := project.NewUsecase(projectRepo)
	projectLifecycle := project.NewLifecycle(projectRepo, recordRepo, hub)
	thresholdUsecase := threshold.NewUsecase(thresholdRepo, projectRepo)
	metricsUsecase := metrics.NewUsecase(recordRepo, snapshotRepo, cfg.MetricsWindow)
	ingestUsecase := ingest.NewUsecase(projectRepo, recordRepo)

	explainer := llm.New(cfg.GeminiAPIKey, cfg.GeminiModel)
	anomalyUsecase := anomaly.NewUsecase(anomalyRepo, thresholdRepo, explainer, hub, cfg.DedupeWindow)
	auditUsecase := audit.NewUsecase(auditRepo, logger)

	worker := anomaly.NewWorker(
		recordRepo,
		metricsUsecase,
		snapshotRepo,
		anomalyUsecase,
		hub,
		cfg.WorkerInterval,
		cfg.SnapshotRetention,
		logger,
	)
	worker.Start()
	defer worker.Stop()

	// Audit retention runs alongside snapshot retention: an hourly pass
	// deletes audit events older than AUDIT_RETENTION.
	auditPruner := audit.NewPruner(auditRepo, time.Hour, cfg.AuditRetention, logger)
	auditPruner.Start()
	defer auditPruner.Stop()

	handler := server.New(cfg, logger, server.Deps{
		Projects:    projectUsecase,
		Lifecycle:   projectLifecycle,
		Thresholds:  thresholdUsecase,
		Metrics:     metricsUsecase,
		Ingest:      ingestUsecase,
		Anomalies:   anomalyUsecase,
		Audit:       auditUsecase,
		ProjectRepo: projectRepo,
		RecordRepo:  recordRepo,
		Hub:         hub,
	})

	httpServer := &http.Server{
		Addr:              cfg.Addr,
		Handler:           handler,
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		IdleTimeout:       120 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("api listening", "addr", cfg.Addr, "env", cfg.Env, "auth_mode", cfg.AuthMode)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	select {
	case err := <-errCh:
		return fmt.Errorf("http server: %w", err)
	case signal := <-stop:
		logger.Info("shutting down", "signal", signal.String())
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()

	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}

	logger.Info("stopped")
	return nil
}

func newLogger(level string) *slog.Logger {
	parsed := slog.LevelInfo
	switch strings.ToLower(level) {
	case "debug":
		parsed = slog.LevelDebug
	case "warn":
		parsed = slog.LevelWarn
	case "error":
		parsed = slog.LevelError
	}

	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: parsed}))
}

// healthcheck is used by the container HEALTHCHECK because the runtime image
// has no shell or curl.
func healthcheck() int {
	addr := os.Getenv("ADDR")
	if addr == "" {
		addr = ":8080"
	}
	if port := os.Getenv("PORT"); port != "" {
		addr = ":" + port
	}

	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		host, port = "127.0.0.1", "8080"
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}

	client := &http.Client{Timeout: 2 * time.Second}
	response, err := client.Get(fmt.Sprintf("http://%s/health", net.JoinHostPort(host, port)))
	if err != nil {
		return 1
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}
