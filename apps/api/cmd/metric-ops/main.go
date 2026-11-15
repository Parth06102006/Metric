// metric-ops is an opt-in R2 backup job and private Prometheus exporter.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"traffic-center/internal/ops"
)

func value(name, def string) string {
	v := os.Getenv(name)
	if v == "" {
		return def
	}
	return v
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "metric-ops: %v\n", err)
		os.Exit(1)
	}
}
func run() error {
	mode := "serve"
	if len(os.Args) > 1 {
		mode = os.Args[1]
	}
	switch mode {
	case "serve", "verify-latest", "list-backups":
		if len(os.Args) > 2 {
			return errors.New("unexpected arguments")
		}
	case "restore-latest":
		if len(os.Args) != 3 {
			return errors.New("restore-latest requires a new destination path")
		}
	case "restore-key":
		if len(os.Args) != 4 {
			return errors.New("restore-key requires an object key and a new destination path")
		}
	default:
		return errors.New("usage: metric-ops [serve|list-backups|verify-latest|restore-latest DESTINATION|restore-key KEY DESTINATION]")
	}
	interval, err := time.ParseDuration(value("BACKUP_INTERVAL", "1h"))
	if err != nil || interval < time.Minute {
		return errors.New("BACKUP_INTERVAL must be a duration of at least 1m")
	}
	timeout, err := time.ParseDuration(value("BACKUP_TIMEOUT", "50m"))
	if err != nil || timeout <= 0 || timeout >= interval {
		return errors.New("BACKUP_TIMEOUT must be positive and less than BACKUP_INTERVAL")
	}
	store, err := ops.NewR2(os.Getenv("R2_ACCOUNT_ID"), os.Getenv("R2_BUCKET"), os.Getenv("R2_ACCESS_KEY_ID"), os.Getenv("R2_SECRET_ACCESS_KEY"), value("R2_PREFIX", "metric/"))
	if err != nil && mode != "serve" {
		return err
	}
	dir := value("BACKUP_WORK_DIR", "/data")
	db := value("DATABASE_PATH", "/data/traffic.db")
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err != nil {
		logger.Error("R2 backup is not configured; disk monitoring remains available", "error", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if mode != "serve" {
		ctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		if mode == "verify-latest" {
			if err := store.VerifyLatest(ctx, dir); err != nil {
				return err
			}
			logger.Info("latest remote backup passed checksum, integrity and schema checks")
			return nil
		}
		if mode == "list-backups" {
			return store.List(ctx, func(key string) error { _, err := fmt.Fprintln(os.Stdout, key); return err })
		}
		var destination string
		if mode == "restore-latest" {
			destination = os.Args[2]
			err = store.RestoreLatest(ctx, destination)
		} else {
			destination = os.Args[3]
			err = store.RestoreKey(ctx, os.Args[2], destination)
		}
		if err != nil {
			return err
		}
		logger.Info("verified backup restored to new path", "path", destination)
		return nil
	}
	service := ops.NewService(db, dir, store, logger)
	service.APIReadyURL = os.Getenv("API_READY_URL")
	mux := http.NewServeMux()
	mux.HandleFunc("GET /metrics", service.Metrics)
	port, err := strconv.Atoi(value("OPS_METRICS_PORT", "9101"))
	if err != nil || port < 1 || port > 65535 {
		return errors.New("invalid OPS_METRICS_PORT")
	}
	server := &http.Server{Addr: fmt.Sprintf(":%d", port), Handler: mux, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second}
	errCh := make(chan error, 1)
	go func() {
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()
	done := make(chan struct{})
	go func() { defer close(done); service.Run(ctx, interval, timeout) }()
	logger.Info("operations service started", "backup_interval", interval.String())
	var serveErr error
	select {
	case err := <-errCh:
		serveErr = fmt.Errorf("serve metrics: %w", err)
		stop()
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	httpErr := server.Shutdown(shutdown)
	select {
	case <-done:
	case <-shutdown.Done():
		return errors.Join(serveErr, httpErr, errors.New("backup shutdown timed out"))
	}
	return errors.Join(serveErr, httpErr)
}
