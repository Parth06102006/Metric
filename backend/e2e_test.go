package main_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"traffic-center/backend/anomaly"
	"traffic-center/backend/api"
	"traffic-center/backend/ingest"
	"traffic-center/backend/llm"
	"traffic-center/backend/metrics"
	"traffic-center/backend/store"
	"traffic-center/backend/ws"
)

func runSimulator(t *testing.T, serverURL, projectID, apiKey, mode string, duration, rps int) {
	t.Helper()

	// Find the project root where simulator/simulator.py lives
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	projectRoot := cwd
	if filepath.Base(projectRoot) == "backend" {
		projectRoot = filepath.Dir(projectRoot)
	}

	simScript := filepath.Join(projectRoot, "simulator", "simulator.py")

	cmdArgs := []string{
		simScript,
		"--url", serverURL,
		"--project-id", projectID,
		"--api-key", apiKey,
		"--duration", fmt.Sprintf("%d", duration),
		"--rps", fmt.Sprintf("%d", rps),
		"--batch-size", "20",
	}
	if mode != "normal" {
		cmdArgs = append(cmdArgs, "--"+mode)
	}

	cmd := exec.Command("python", cmdArgs...)
	cmd.Dir = projectRoot
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("simulator (%s) failed: %v\nOutput: %s", mode, err, string(out))
	}
}

func setupIsolatedE2EServer(t *testing.T) (*httptest.Server, *store.Store, *ws.Hub, *anomaly.PipelineWorker, string, string) {
	t.Helper()
	tempDB := filepath.Join(t.TempDir(), "e2e_isolated.db")
	db, err := store.Open(tempDB)
	if err != nil {
		t.Fatalf("Open test DB failed: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	ingestAPI := &ingest.Handler{
		Authenticator: db,
		Projects:      make(map[string]ingest.ProjectTarget),
		Buffers:       make(map[string]*metrics.Buffer),
	}

	projectAPI := &api.Handler{
		Store:   db,
		Buffers: ingestAPI,
	}

	hub := ws.NewHub()

	wsAPI := &ws.Handler{
		Store:   db,
		Buffers: ingestAPI,
		Hub:     hub,
	}

	// Explainer with empty API key so it exercises deterministic fallback
	explainer := &llm.Client{
		APIKey:          "",
		RateLimitWindow: 60 * time.Second,
	}

	// Fast 100ms interval for E2E responsiveness
	worker := anomaly.NewWorker(db, ingestAPI, explainer, hub, 100*time.Millisecond)
	worker.Start()
	t.Cleanup(func() { worker.Stop() })

	mux := http.NewServeMux()
	mux.Handle("/api/projects", projectAPI)
	mux.Handle("/api/projects/", projectAPI)
	mux.Handle("/ingest/", ingestAPI)
	mux.Handle("/ws/", wsAPI)

	server := httptest.NewServer(mux)
	t.Cleanup(func() { server.Close() })

	// Create a test project
	proj, key, err := db.CreateProject("E2E Test Project")
	if err != nil {
		t.Fatalf("CreateProject failed: %v", err)
	}

	return server, db, hub, worker, proj.ID, key
}

func TestE2E_AllScenarios(t *testing.T) {
	server, db, _, worker, projectID, apiKey := setupIsolatedE2EServer(t)

	// Setup WebSocket connection to listen for real-time events
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws/" + projectID
	wsConn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial WS failed: %v", err)
	}
	defer wsConn.Close()

	// Read initial snapshot event from WS
	var initEnv ws.EventEnvelope
	if err := wsConn.ReadJSON(&initEnv); err != nil {
		t.Fatalf("read init WS event failed: %v", err)
	}
	if initEnv.EventType != ws.EventMetricSnapshot || initEnv.ProjectID != projectID {
		t.Fatalf("unexpected init WS event: %+v", initEnv)
	}

	// -------------------------------------------------------------
	// Scenario 1: Normal Traffic
	// -------------------------------------------------------------
	t.Run("Scenario 1: Normal Traffic", func(t *testing.T) {
		// Set thresholds high so normal traffic does NOT trigger anomalies
		err := db.UpdateThresholds(projectID, store.Thresholds{
			P95Ms:       1000.0,
			ErrorRate:   50.0,
			RPSMax:      500.0,
			ReqPerIPMax: 100.0,
		})
		if err != nil {
			t.Fatalf("set thresholds: %v", err)
		}

		runSimulator(t, server.URL, projectID, apiKey, "normal", 3, 10)
		time.Sleep(300 * time.Millisecond)
		worker.EvaluateProject(projectID)

		// Verify metric snapshots exist in isolated SQLite DB
		var snapCount int
		if err := db.DB.QueryRow(`SELECT COUNT(1) FROM metric_snapshots WHERE project_id = ?`, projectID).Scan(&snapCount); err != nil {
			t.Fatalf("query snapshots: %v", err)
		}
		if snapCount == 0 {
			t.Fatalf("expected metric snapshots to be persisted, got 0")
		}

		// Verify 0 anomalies triggered
		var anomCount int
		if err := db.DB.QueryRow(`SELECT COUNT(1) FROM anomalies WHERE project_id = ?`, projectID).Scan(&anomCount); err != nil {
			t.Fatalf("query anomalies: %v", err)
		}
		if anomCount != 0 {
			t.Fatalf("expected 0 anomalies for normal traffic, got %d", anomCount)
		}
	})

	// -------------------------------------------------------------
	// Scenario 2: Spike Traffic (Elevated Latency)
	// -------------------------------------------------------------
	t.Run("Scenario 2: Spike Traffic", func(t *testing.T) {
		// Set low P95 threshold so spike triggers high_latency
		err := db.UpdateThresholds(projectID, store.Thresholds{
			P95Ms:       20.0, // Spike generates 50ms-400ms latency
			ErrorRate:   80.0,
			RPSMax:      500.0,
			ReqPerIPMax: 100.0,
		})
		if err != nil {
			t.Fatalf("set thresholds: %v", err)
		}

		runSimulator(t, server.URL, projectID, apiKey, "spike", 3, 15)
		time.Sleep(300 * time.Millisecond)
		worker.EvaluateProject(projectID)

		var highLatCount int
		err = db.DB.QueryRow(
			`SELECT COUNT(1) FROM anomalies WHERE project_id = ? AND summary LIKE 'high_latency:%'`,
			projectID,
		).Scan(&highLatCount)
		if err != nil {
			t.Fatalf("query spike anomalies: %v", err)
		}
		if highLatCount == 0 {
			t.Fatalf("expected high_latency anomaly to be triggered and persisted for spike traffic")
		}
	})

	// -------------------------------------------------------------
	// Scenario 3: Error Traffic (Elevated 5xx Errors)
	// -------------------------------------------------------------
	t.Run("Scenario 3: Error Traffic", func(t *testing.T) {
		// Set low error rate threshold
		err := db.UpdateThresholds(projectID, store.Thresholds{
			P95Ms:       2000.0,
			ErrorRate:   5.0, // Errors mode generates ~70% 5xx
			RPSMax:      500.0,
			ReqPerIPMax: 100.0,
		})
		if err != nil {
			t.Fatalf("set thresholds: %v", err)
		}

		runSimulator(t, server.URL, projectID, apiKey, "errors", 3, 15)
		time.Sleep(300 * time.Millisecond)
		worker.EvaluateProject(projectID)

		var errorCount int
		err = db.DB.QueryRow(
			`SELECT COUNT(1) FROM anomalies WHERE project_id = ? AND summary LIKE 'high_error_rate:%'`,
			projectID,
		).Scan(&errorCount)
		if err != nil {
			t.Fatalf("query error anomalies: %v", err)
		}
		if errorCount == 0 {
			t.Fatalf("expected high_error_rate anomaly to be triggered and persisted for errors traffic")
		}
	})

	// -------------------------------------------------------------
	// Scenario 4: Flood Traffic (Concentrated Client IP Flood)
	// -------------------------------------------------------------
	t.Run("Scenario 4: Flood Traffic", func(t *testing.T) {
		// Set low IP RPS threshold (in a 3-second test, 60 records over 60s window gives ~0.2-0.3 TopIPRPS)
		err := db.UpdateThresholds(projectID, store.Thresholds{
			P95Ms:       2000.0,
			ErrorRate:   80.0,
			RPSMax:      500.0,
			ReqPerIPMax: 0.1, // Flood concentrates traffic onto 5 IPs
		})
		if err != nil {
			t.Fatalf("set thresholds: %v", err)
		}

		runSimulator(t, server.URL, projectID, apiKey, "flood", 3, 20)
		time.Sleep(300 * time.Millisecond)
		worker.EvaluateProject(projectID)

		var floodCount int
		err = db.DB.QueryRow(
			`SELECT COUNT(1) FROM anomalies WHERE project_id = ? AND summary LIKE 'possible_ip_flood:%'`,
			projectID,
		).Scan(&floodCount)
		if err != nil {
			t.Fatalf("query flood anomalies: %v", err)
		}
		if floodCount == 0 {
			t.Fatalf("expected possible_ip_flood anomaly to be triggered and persisted for flood traffic")
		}
	})

	// -------------------------------------------------------------
	// Scenario 5: Deterministic Fallback Explanation Verification
	// -------------------------------------------------------------
	t.Run("Scenario 5: LLM Fallback Explanation", func(t *testing.T) {
		var analysis string
		err := db.DB.QueryRow(
			`SELECT analysis FROM anomalies WHERE project_id = ? ORDER BY id DESC LIMIT 1`,
			projectID,
		).Scan(&analysis)
		if err != nil {
			t.Fatalf("query analysis: %v", err)
		}
		if !strings.HasPrefix(analysis, "[Rule-Based Analysis]") {
			t.Fatalf("expected deterministic [Rule-Based Analysis] prefix in persisted analysis, got: %s", analysis)
		}
	})

	// -------------------------------------------------------------
	// Scenario 6: WebSocket Events, Envelope & Deduplication
	// -------------------------------------------------------------
	t.Run("Scenario 6: WebSocket Events & Deduplication", func(t *testing.T) {
		// Count anomalies before repeated evaluation
		var countBefore int
		_ = db.DB.QueryRow(`SELECT COUNT(1) FROM anomalies WHERE project_id = ?`, projectID).Scan(&countBefore)

		// Re-evaluate immediately without waiting for DedupeWindow
		worker.EvaluateProject(projectID)

		var countAfter int
		_ = db.DB.QueryRow(`SELECT COUNT(1) FROM anomalies WHERE project_id = ?`, projectID).Scan(&countAfter)

		if countAfter != countBefore {
			t.Fatalf("deduplication failed: anomaly count increased from %d to %d on consecutive tick", countBefore, countAfter)
		}

		// Read one event from WS to confirm envelope format
		_ = wsConn.SetReadDeadline(time.Now().Add(2 * time.Second))
		var env ws.EventEnvelope
		err := wsConn.ReadJSON(&env)
		if err != nil {
			t.Fatalf("read websocket event failed: %v", err)
		}

		if env.EventType == "" || env.ProjectID != projectID || env.Timestamp.IsZero() {
			t.Fatalf("invalid envelope format received over WS: %+v", env)
		}
	})
}
