package server_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"traffic-center/domain"
	"traffic-center/internal/config"
	"traffic-center/internal/httpx"
	"traffic-center/internal/ws"
)

func rawRequest(t *testing.T, method, url, contentType string, body []byte, headers map[string]string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, data
}

func TestHTTPAuthenticationWithTrustedProxySecret(t *testing.T) {
	server, _ := newProxyTestServerWithConfig(t, []string{"admin@example.com"}, func(cfg *config.Config) {
		cfg.ProxySecret = "test-proxy-secret"
	})
	for _, tc := range []struct {
		name    string
		headers map[string]string
		want    int
	}{
		{"no credentials", nil, http.StatusUnauthorized},
		{"spoofed identity without secret", map[string]string{"X-Auth-Request-Email": "admin@example.com"}, http.StatusUnauthorized},
		{"wrong secret", map[string]string{"X-Auth-Request-Email": "admin@example.com", "X-Proxy-Secret": "wrong"}, http.StatusUnauthorized},
		{"secret without identity", map[string]string{"X-Proxy-Secret": "test-proxy-secret"}, http.StatusUnauthorized},
		{"valid admin", map[string]string{"X-Auth-Request-Email": "admin@example.com", "X-Proxy-Secret": "test-proxy-secret"}, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp, body := doJSON(t, http.MethodGet, server.URL+"/api/auth/me", nil, tc.headers)
			if resp.StatusCode != tc.want {
				t.Fatalf("status = %d, want %d: %s", resp.StatusCode, tc.want, body)
			}
		})
	}
	user := map[string]string{"X-Auth-Request-Email": "user@example.com", "X-Proxy-Secret": "test-proxy-secret"}
	resp, body := doJSON(t, http.MethodPost, server.URL+"/api/projects", map[string]string{"name": "not allowed"}, user)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("non-admin create = %d: %s", resp.StatusCode, body)
	}
}

func TestHTTPIngestAuthenticationAndValidation(t *testing.T) {
	server := newTestServer(t)
	first := createProject(t, server.URL, "Ingest A")
	second := createProject(t, server.URL, "Ingest B")
	validRecords := sampleRecords()
	validBody, err := json.Marshal(map[string]any{"logs": validRecords})
	if err != nil {
		t.Fatal(err)
	}
	url := server.URL + "/ingest/" + first.ProjectID
	for _, tc := range []struct{ name, target, key string }{
		{"missing key", url, ""},
		{"invalid key", url, "wrong"},
		{"other project's key", url, second.APIKey},
		{"unknown project", server.URL + "/ingest/unknown", first.APIKey},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, body := rawRequest(t, http.MethodPost, tc.target, "application/json", validBody, map[string]string{"X-API-Key": tc.key})
			if status != http.StatusUnauthorized {
				t.Fatalf("status = %d: %s", status, body)
			}
			if bytes.Contains(body, []byte(tc.key)) && tc.key != "" {
				t.Fatal("response leaked API key")
			}
		})
	}
	invalid := validRecords[0]
	invalid.IP = "not-an-ip"
	mixed, _ := json.Marshal(map[string]any{"logs": []domain.LogRecord{validRecords[0], invalid}})
	tooMany := make([]domain.LogRecord, 1001)
	for i := range tooMany {
		tooMany[i] = validRecords[0]
	}
	tooManyBody, _ := json.Marshal(map[string]any{"logs": tooMany})
	for _, tc := range []struct {
		name, contentType string
		body              []byte
		want              int
	}{
		{"missing content type", "", validBody, http.StatusUnsupportedMediaType},
		{"malformed JSON", "application/json", []byte(`{"logs":`), http.StatusBadRequest},
		{"multiple JSON objects", "application/json", append(append([]byte{}, validBody...), []byte(` {}`)...), http.StatusBadRequest},
		{"empty batch", "application/json", []byte(`{"logs":[]}`), http.StatusBadRequest},
		{"batch over limit", "application/json", tooManyBody, http.StatusBadRequest},
		{"mixed valid and invalid records", "application/json", mixed, http.StatusBadRequest},
		{"body over byte limit", "application/json", []byte(`{"logs":[],"padding":"` + strings.Repeat("x", httpx.MaxRequestBody) + `"}`), http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, body := rawRequest(t, http.MethodPost, url, tc.contentType, tc.body, map[string]string{"X-API-Key": first.APIKey})
			if status != tc.want {
				t.Fatalf("status = %d, want %d: %s", status, tc.want, body)
			}
			var response struct {
				Error string `json:"error"`
			}
			if err := json.Unmarshal(body, &response); err != nil || response.Error == "" {
				t.Fatalf("invalid error response: %s (%v)", body, err)
			}
		})
	}
	for _, tc := range []struct {
		name   string
		mutate func(*domain.LogRecord)
	}{
		{"missing timestamp", func(r *domain.LogRecord) { r.Ts = time.Time{} }},
		{"missing endpoint", func(r *domain.LogRecord) { r.Endpoint = " " }},
		{"missing method", func(r *domain.LogRecord) { r.Method = "" }},
		{"status below range", func(r *domain.LogRecord) { r.Status = 99 }},
		{"status above range", func(r *domain.LogRecord) { r.Status = 600 }},
		{"negative latency", func(r *domain.LogRecord) { r.LatencyMs = -1 }},
		{"negative payload", func(r *domain.LogRecord) { r.PayloadBytes = -1 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			record := validRecords[0]
			tc.mutate(&record)
			payload, err := json.Marshal(map[string]any{"logs": []domain.LogRecord{record}})
			if err != nil {
				t.Fatal(err)
			}
			status, body := rawRequest(t, http.MethodPost, url, "application/json", payload, map[string]string{"X-API-Key": first.APIKey})
			if status != http.StatusBadRequest {
				t.Fatalf("invalid record status = %d: %s", status, body)
			}
		})
	}
	resp, body := doJSON(t, http.MethodGet, server.URL+"/api/projects/"+first.ProjectID+"/metrics", nil, nil)
	var before domain.Snapshot
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &before) != nil || before.RPS != 0 {
		t.Fatalf("rejected batches changed metrics: %s", body)
	}
	status, body := rawRequest(t, http.MethodPost, url, "application/json; charset=utf-8", validBody, map[string]string{"X-API-Key": first.APIKey})
	if status != http.StatusAccepted {
		t.Fatalf("valid batch = %d: %s", status, body)
	}
	var accepted struct {
		Accepted int `json:"accepted"`
	}
	if err := json.Unmarshal(body, &accepted); err != nil || accepted.Accepted != len(validRecords) {
		t.Fatalf("accepted response: %s (%v)", body, err)
	}
	maxBatchBody, err := json.Marshal(map[string]any{"logs": tooMany[:1000]})
	if err != nil {
		t.Fatal(err)
	}
	status, body = rawRequest(t, http.MethodPost, url, "application/json", maxBatchBody, map[string]string{"X-API-Key": first.APIKey})
	if status != http.StatusAccepted || json.Unmarshal(body, &accepted) != nil || accepted.Accepted != 1000 {
		t.Fatalf("maximum batch rejected: %d %s", status, body)
	}
}

func TestHTTPProjectAndThresholdValidation(t *testing.T) {
	server := newTestServer(t)
	project := createProject(t, server.URL, "Validation")
	thresholdURL := server.URL + "/api/projects/" + project.ProjectID + "/thresholds"
	for _, tc := range []struct {
		name, method, target, contentType, body string
		want                                    int
	}{
		{"empty project name", "POST", server.URL + "/api/projects", "application/json", `{"name":"  "}`, 400},
		{"malformed project", "POST", server.URL + "/api/projects", "application/json", `{"name":`, 400},
		{"project without JSON content type", "POST", server.URL + "/api/projects", "text/plain", `{"name":"test"}`, 400},
		{"negative latency threshold", "PUT", thresholdURL, "application/json", `{"p95_ms":-1}`, 400},
		{"error percentage over 100", "PUT", thresholdURL, "application/json", `{"error_rate":101}`, 400},
		{"negative RPS", "PUT", thresholdURL, "application/json", `{"rps_max":-1}`, 400},
		{"negative per-IP RPS", "PUT", thresholdURL, "application/json", `{"req_per_ip_max":-1}`, 400},
		{"multiple threshold objects", "PUT", thresholdURL, "application/json", `{} {}`, 400},
		{"invalid owner email", "PATCH", server.URL + "/api/projects/" + project.ProjectID, "application/json", `{"owner_email":"invalid"}`, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, body := rawRequest(t, tc.method, tc.target, tc.contentType, []byte(tc.body), nil)
			if status != tc.want {
				t.Fatalf("status = %d, want %d: %s", status, tc.want, body)
			}
		})
	}
	resp, body := doJSON(t, http.MethodGet, thresholdURL, nil, nil)
	var got domain.Threshold
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &got) != nil || got != domain.DefaultThreshold() {
		t.Fatalf("invalid updates changed thresholds: %s", body)
	}
}

func TestWebSocketOwnershipChangeRevokesExistingStreams(t *testing.T) {
	server, _ := newProxyTestServerWithConfig(t, []string{"admin@example.com"}, func(cfg *config.Config) {
		cfg.ProxySecret = "test-proxy-secret"
	})
	admin := map[string]string{"X-Auth-Request-Email": "admin@example.com", "X-Proxy-Secret": "test-proxy-secret"}
	project := createProjectAs(t, server.URL, admin, "Live handover")
	ownerURL := server.URL + "/api/projects/" + project.ProjectID
	assign := func(owner string) {
		t.Helper()
		resp, body := doJSON(t, http.MethodPatch, ownerURL, map[string]string{"owner_email": owner}, admin)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("assign owner: %d %s", resp.StatusCode, body)
		}
	}
	assign("alice@example.com")
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws/" + project.ProjectID
	dial := func(email string) *websocket.Conn {
		t.Helper()
		headers := http.Header{"X-Auth-Request-Email": []string{email}, "X-Proxy-Secret": []string{"test-proxy-secret"}}
		conn, _, err := websocket.DefaultDialer.Dial(wsURL, headers)
		if err != nil {
			t.Fatalf("dial %s: %v", email, err)
		}
		t.Cleanup(func() { conn.Close() })
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		var event ws.Envelope
		if err := conn.ReadJSON(&event); err != nil {
			t.Fatalf("initial event: %v", err)
		}
		if event.EventType != ws.EventMetricSnapshot || event.ProjectID != project.ProjectID {
			t.Fatalf("initial event: %+v", event)
		}
		return conn
	}
	alice := dial("alice@example.com")
	adminConn := dial("admin@example.com")
	assign("bob@example.com")
	for _, conn := range []*websocket.Conn{alice, adminConn} {
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, _, err := conn.ReadMessage()
		if err == nil {
			t.Fatal("old subscriber remained connected")
		}
		if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
			t.Fatal("old subscriber was not closed after ownership change")
		}
	}
	aliceHeaders := http.Header{"X-Auth-Request-Email": []string{"alice@example.com"}, "X-Proxy-Secret": []string{"test-proxy-secret"}}
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, aliceHeaders)
	if conn != nil {
		conn.Close()
	}
	if err == nil || resp == nil || resp.StatusCode != http.StatusNotFound {
		t.Fatalf("old owner reconnect: %v %v", resp, err)
	}
	if resp.Body != nil {
		resp.Body.Close()
	}
	dial("bob@example.com")
	dial("admin@example.com")
}

func TestWebSocketProxyAuthenticationAndOriginPolicy(t *testing.T) {
	server, _ := newProxyTestServerWithConfig(t, []string{"admin@example.com"}, func(cfg *config.Config) {
		cfg.ProxySecret = "test-proxy-secret"
		cfg.AllowedOrigins = map[string]struct{}{"trusted.example": {}}
	})
	admin := map[string]string{"X-Auth-Request-Email": "admin@example.com", "X-Proxy-Secret": "test-proxy-secret"}
	project := createProjectAs(t, server.URL, admin, "Socket security")
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws/" + project.ProjectID
	sameOrigin := server.URL
	for _, tc := range []struct {
		name, email, secret, origin string
		want                        int
	}{
		{"missing identity", "", "test-proxy-secret", "", 401},
		{"spoofed admin", "admin@example.com", "wrong", "", 401},
		{"other user", "alice@example.com", "test-proxy-secret", "", 404},
		{"blocked origin", "admin@example.com", "test-proxy-secret", "https://evil.example", 403},
		{"null origin", "admin@example.com", "test-proxy-secret", "null", 403},
		{"no origin", "admin@example.com", "test-proxy-secret", "", 101},
		{"same origin", "admin@example.com", "test-proxy-secret", sameOrigin, 101},
		{"allowed origin", "admin@example.com", "test-proxy-secret", "https://trusted.example", 101},
	} {
		t.Run(tc.name, func(t *testing.T) {
			headers := http.Header{}
			if tc.email != "" {
				headers.Set("X-Auth-Request-Email", tc.email)
			}
			if tc.secret != "" {
				headers.Set("X-Proxy-Secret", tc.secret)
			}
			if tc.origin != "" {
				headers.Set("Origin", tc.origin)
			}
			conn, resp, err := websocket.DefaultDialer.Dial(wsURL, headers)
			if tc.want != http.StatusSwitchingProtocols {
				if conn != nil {
					conn.Close()
				}
				if err == nil || resp == nil || resp.StatusCode != tc.want {
					t.Fatalf("dial status = %v, error = %v, want %d", resp, err, tc.want)
				}
				if resp.Body != nil {
					resp.Body.Close()
				}
				return
			}
			if err != nil {
				t.Fatalf("dial: %v", err)
			}
			defer conn.Close()
			_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
			var event ws.Envelope
			if err := conn.ReadJSON(&event); err != nil {
				t.Fatal(err)
			}
			if event.EventType != ws.EventMetricSnapshot || event.ProjectID != project.ProjectID {
				t.Fatalf("unexpected event: %+v", event)
			}
		})
	}
}
