package server_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestProjectViewersRequireKnownIdentityAndRevokeStream(t *testing.T) {
	srv, _ := newProxyTestServerWithConfig(t, []string{"admin@example.com"}, nil)
	admin := map[string]string{"X-Auth-Request-Email": "admin@example.com"}
	viewer := map[string]string{"X-Auth-Request-Email": "viewer@example.com"}
	outsider := map[string]string{"X-Auth-Request-Email": "outsider@example.com"}
	resp, body := doJSON(t, http.MethodPost, srv.URL+"/api/projects", map[string]string{"name": "Shared"}, admin)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d %s", resp.StatusCode, body)
	}
	var p struct {
		ID string `json:"project_id"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatal(err)
	}
	base := srv.URL + "/api/projects/" + p.ID
	for _, tc := range []struct {
		method, path string
		body         any
		headers      map[string]string
		want         int
	}{
		{http.MethodPost, base + "/viewers", map[string]string{"email": "viewer@example.com"}, admin, http.StatusNotFound},
		{http.MethodGet, base, nil, viewer, http.StatusNotFound},
		{http.MethodGet, srv.URL + "/api/users", nil, viewer, http.StatusForbidden},
		{http.MethodGet, srv.URL + "/api/auth/me", nil, viewer, http.StatusOK},
		{http.MethodPost, base + "/viewers", map[string]string{"email": "VIEWER@example.com"}, admin, http.StatusNoContent},
		{http.MethodGet, base, nil, viewer, http.StatusOK},
		{http.MethodGet, base + "/thresholds", nil, viewer, http.StatusOK},
		{http.MethodGet, base + "/metrics/history", nil, viewer, http.StatusOK},
		{http.MethodGet, base + "/anomalies", nil, viewer, http.StatusOK},
		{http.MethodGet, base, nil, outsider, http.StatusNotFound},
		{http.MethodPost, base + "/api-key", nil, viewer, http.StatusForbidden},
		{http.MethodDelete, base + "/viewers", map[string]string{"email": "viewer@example.com"}, viewer, http.StatusForbidden},
	} {
		r, b := doJSON(t, tc.method, tc.path, tc.body, tc.headers)
		if r.StatusCode != tc.want {
			t.Fatalf("%s %s: %d want %d: %s", tc.method, tc.path, r.StatusCode, tc.want, b)
		}
	}
	list, b := doJSON(t, http.MethodGet, srv.URL+"/api/projects", nil, viewer)
	if list.StatusCode != http.StatusOK || !strings.Contains(string(b), p.ID) {
		t.Fatalf("viewer list: %d %s", list.StatusCode, b)
	}
	headers := http.Header{}
	headers.Set("X-Auth-Request-Email", "viewer@example.com")
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/"+p.ID, headers)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, _, err := conn.ReadMessage(); err != nil {
		t.Fatalf("initial snapshot: %v", err)
	}
	r, b := doJSON(t, http.MethodDelete, base+"/viewers", map[string]string{"email": "viewer@example.com"}, admin)
	if r.StatusCode != http.StatusNoContent {
		t.Fatalf("remove: %d %s", r.StatusCode, b)
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, _, err := conn.ReadMessage(); err == nil {
		t.Fatal("revoked stream remains open")
	}
	r, b = doJSON(t, http.MethodGet, base, nil, viewer)
	if r.StatusCode != http.StatusNotFound {
		t.Fatalf("removed user access: %d %s", r.StatusCode, b)
	}
}
