package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"traffic-center/backend/anomaly"
	"traffic-center/backend/api"
	"traffic-center/backend/ingest"
	"traffic-center/backend/llm"
	"traffic-center/backend/metrics"
	"traffic-center/backend/store"
	websocketapi "traffic-center/backend/ws"
)

func main() {
	databasePath := os.Getenv("DATABASE_PATH")
	if databasePath == "" {
		databasePath = "traffic.db"
	}

	db, err := store.Open(databasePath)
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	defer db.Close()

	ingestAPI := &ingest.Handler{
		Authenticator: db,
		Projects:      make(map[string]ingest.ProjectTarget),
		Buffers:       make(map[string]*metrics.Buffer),
	}

	projectAPI := &api.Handler{
		Store:   db,
		Buffers: ingestAPI,
	}

	hub := websocketapi.NewHub()

	wsAPI := &websocketapi.Handler{
		Store:   db,
		Buffers: ingestAPI,
		Hub:     hub,
	}

	// Initialize LLM explainer and background evaluation worker
	explainer := llm.NewClient()
	worker := anomaly.NewWorker(db, ingestAPI, explainer, hub, 5*time.Second)
	worker.Start()
	defer worker.Stop()

	mux := http.NewServeMux()
	mux.Handle("/api/projects", projectAPI)
	mux.Handle("/api/projects/", projectAPI)
	mux.Handle("/ingest/", ingestAPI)
	mux.Handle("/ws/", wsAPI)

	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	server := &http.Server{
		Addr:              ":8080",
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	// Graceful shutdown handling
	stopSig := make(chan os.Signal, 1)
	signal.Notify(stopSig, os.Interrupt, syscall.SIGTERM)

	go func() {
		log.Println("AI Traffic Command Center listening on http://localhost:8080")
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server error: %v", err)
		}
	}()

	<-stopSig
	log.Println("Shutting down AI Traffic Command Center gracefully...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("HTTP server shutdown failed: %v", err)
	}
	log.Println("Server stopped.")
}
