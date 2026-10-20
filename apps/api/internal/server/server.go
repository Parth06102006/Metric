package server

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"

	"traffic-center/anomaly"
	"traffic-center/domain"
	"traffic-center/ingest"
	"traffic-center/internal/config"
	"traffic-center/internal/httpx"
	"traffic-center/internal/middleware"
	"traffic-center/internal/rest"
	"traffic-center/internal/ws"
	"traffic-center/metrics"
	"traffic-center/project"
	"traffic-center/threshold"
)

// Deps is the set of feature usecases and adapters the router needs.
type Deps struct {
	Projects    *project.Usecase
	Thresholds  *threshold.Usecase
	Metrics     *metrics.Usecase
	Ingest      *ingest.Usecase
	Anomalies   *anomaly.Usecase
	ProjectRepo domain.ProjectRepository
	RecordRepo  domain.RecordRepository
	Hub         *ws.Hub
}

// New builds the complete HTTP handler.
func New(cfg config.Config, logger *slog.Logger, deps Deps) http.Handler {
	router := chi.NewRouter()

	// Global chain: correlating ID, TLS/edge concerns, logging, panic safety.
	router.Use(middleware.RequestID)
	router.Use(chimw.StripSlashes)
	router.Use(middleware.RealIP(cfg.TrustProxy))
	router.Use(middleware.Logger(logger, cfg.AuthEmailHeader))
	router.Use(middleware.Recoverer(logger))

	router.NotFound(func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, http.StatusNotFound, httpx.ErrorResponse{Error: "route not found"})
	})
	router.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, http.StatusMethodNotAllowed, httpx.ErrorResponse{Error: "method not allowed"})
	})

	identity := middleware.Identity(middleware.IdentityConfig{
		Mode:        cfg.AuthMode,
		EmailHeader: cfg.AuthEmailHeader,
		UserHeader:  cfg.AuthUserHeader,
		ProxySecret: cfg.ProxySecret,
		DevEmail:    cfg.DevEmail,
		AdminEmails: cfg.AdminEmails,
	})

	healthHandler := rest.NewHealthHandler()
	identityHandler := rest.NewIdentityHandler()
	projectHandler := rest.NewProjectHandler(deps.Projects)
	thresholdHandler := rest.NewThresholdHandler(deps.Thresholds)
	metricsHandler := rest.NewMetricsHandler(deps.Projects, deps.Metrics)
	anomalyHandler := rest.NewAnomalyHandler(deps.Projects, deps.Anomalies)
	ingestHandler := rest.NewIngestHandler(deps.Ingest)
	wsHandler := ws.NewHandler(deps.ProjectRepo, deps.RecordRepo, deps.Hub, cfg.MetricsWindow, cfg.AllowedOrigins, logger)

	// Public realm: liveness and machine ingest (X-API-Key), no SSO.
	router.Get("/health", healthHandler.Health)
	router.Post("/ingest/{projectID}", ingestHandler.Ingest)

	// Control plane: trusted identity from the edge proxy.
	router.Route("/api", func(r chi.Router) {
		r.Use(identity)
		r.Use(middleware.Timeout(cfg.APITimeout))

		r.Get("/auth/me", identityHandler.Me)

		r.Get("/projects", projectHandler.List)
		r.With(middleware.RequireAdmin).Post("/projects", projectHandler.Create)
		r.Get("/projects/{projectID}", projectHandler.Get)
		r.Get("/projects/{projectID}/thresholds", thresholdHandler.Get)
		r.With(middleware.RequireAdmin).Put("/projects/{projectID}/thresholds", thresholdHandler.Update)
		r.Get("/projects/{projectID}/metrics", metricsHandler.Get)
		r.Get("/projects/{projectID}/anomalies", anomalyHandler.List)
	})

	// Live stream: identity at handshake, no request timeout.
	router.Route("/ws", func(r chi.Router) {
		r.Use(identity)
		r.Get("/{projectID}", wsHandler.ServeHTTP)
	})

	return router
}
