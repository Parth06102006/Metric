package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"traffic-center/domain"
	dbsqlite "traffic-center/internal/sqlite"
)

func TestHTTPAcceptedRecordSurvivesProcessKill(t *testing.T) {
	if os.Getenv("METRIC_DURABILITY_CHILD") == "1" {
		if err := run(); err != nil {
			t.Fatal(err)
		}
		return
	}
	path := filepath.Join(t.TempDir(), "traffic.db")
	db, err := dbsqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO projects(id,name,api_key_hash,owner_email) VALUES(?,?,?,?)`, "p", "project", domain.HashAPIKey("test-key"), "dev@localhost"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	_ = listener.Close()
	client := &http.Client{Timeout: time.Second}
	base := "http://" + addr
	start := func() *exec.Cmd {
		cmd := exec.Command(os.Args[0], "-test.run=^TestHTTPAcceptedRecordSurvivesProcessKill$")
		// Append last so these settings replace any ambient development configuration.
		env := os.Environ()
		for _, v := range []string{"METRIC_DURABILITY_CHILD=1", "ADDR=" + addr, "PORT=", "DATABASE_PATH=" + path, "APP_ENV=development", "AUTH_MODE=dev", "DEV_USER_EMAIL=dev@localhost", "ADMIN_EMAILS=dev@localhost", "GEMINI_API_KEY=", "SQLITE_MAX_BYTES=16777216", "MIN_FREE_DISK_BYTES=1048576"} {
			key, _, _ := strings.Cut(v, "=")
			filtered := env[:0]
			for _, e := range env {
				if !strings.HasPrefix(e, key+"=") {
					filtered = append(filtered, e)
				}
			}
			env = append(filtered, v)
		}
		cmd.Env = env
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			res, err := client.Get(base + "/health")
			if err == nil {
				_ = res.Body.Close()
				if res.StatusCode == 200 {
					return cmd
				}
			}
			time.Sleep(20 * time.Millisecond)
		}
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatal("API failed to start")
		return nil
	}
	kill := func(cmd *exec.Cmd) { _ = cmd.Process.Kill(); _ = cmd.Wait() }
	first := start()
	body, _ := json.Marshal(map[string]any{"logs": []domain.LogRecord{{Ts: time.Now().UTC(), Endpoint: "/items", Method: "GET", Status: 200, IP: "192.0.2.3", LatencyMs: 42}}})
	req, _ := http.NewRequest(http.MethodPost, base+"/ingest/p", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", "test-key")
	res, err := client.Do(req)
	if err != nil {
		kill(first)
		t.Fatal(err)
	}
	data, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusAccepted {
		kill(first)
		t.Fatalf("ingest status %d %s", res.StatusCode, data)
	}
	kill(first)
	second := start()
	defer kill(second)
	res, err = client.Get(base + "/api/projects/p/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != 200 {
		data, _ := io.ReadAll(res.Body)
		t.Fatalf("metrics status %d: %s", res.StatusCode, data)
	}
	var snapshot domain.Snapshot
	if err := json.NewDecoder(res.Body).Decode(&snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.RPS <= 0 || snapshot.P95 != 42 || snapshot.Endpoints["/items"] != 1 {
		t.Fatalf("missing recovered metrics: %+v", snapshot)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("database permissions: %v %v", info, err)
	}
}
