package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"traffic-center/domain"
	"traffic-center/internal/config"
	metricssqlite "traffic-center/metrics/repository/sqlite"
)

func TestMetricHistoryAuthorizationAndBound(t *testing.T) {
	srv, helper := newProxyTestServerWithConfig(t, []string{"admin@example.com"}, func(cfg *config.Config) { cfg.ProxySecret = "secret" })
	admin := map[string]string{"X-Auth-Request-Email": "admin@example.com", "X-Proxy-Secret": "secret"}
	alice := map[string]string{"X-Auth-Request-Email": "alice@example.com", "X-Proxy-Secret": "secret"}
	bob := map[string]string{"X-Auth-Request-Email": "bob@example.com", "X-Proxy-Secret": "secret"}
	p := createProjectAs(t, srv.URL, admin, "Metrics history")
	url := srv.URL + "/api/projects/" + p.ProjectID
	resp, body := doJSON(t, http.MethodPatch, url, map[string]string{"owner_email": "alice@example.com"}, admin)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("assign owner: %d %s", resp.StatusCode, body)
	}
	repo := metricssqlite.New(helper.DB)
	base := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 905; i++ {
		if err := repo.Save(context.Background(), p.ProjectID, domain.Snapshot{Ts: base.Add(time.Duration(i) * time.Second), RPS: float64(i), Endpoints: map[string]int{}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := repo.Save(context.Background(), "other", domain.Snapshot{Ts: base.Add(1000 * time.Second), RPS: 9999, Endpoints: map[string]int{}}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		headers map[string]string
		status  int
	}{
		{"anonymous", nil, http.StatusUnauthorized}, {"other user", bob, http.StatusNotFound},
		{"owner", alice, http.StatusOK}, {"admin", admin, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp, body := doJSON(t, http.MethodGet, url+"/metrics/history?limit=100000", nil, tc.headers)
			if resp.StatusCode != tc.status {
				t.Fatalf("status: %d %s", resp.StatusCode, body)
			}
			if tc.headers != nil && resp.Header.Get("Cache-Control") != "no-store" {
				t.Fatalf("history cache control: %q", resp.Header.Get("Cache-Control"))
			}
			if tc.status != http.StatusOK {
				return
			}
			var got []domain.Snapshot
			if err := json.Unmarshal(body, &got); err != nil {
				t.Fatal(err)
			}
			if len(got) != 900 {
				t.Fatalf("got %d snapshots", len(got))
			}
			for i, snap := range got {
				if snap.RPS != float64(i+5) {
					t.Fatalf("index %d: %+v", i, snap)
				}
			}
		})
	}
	resp, _ = doJSON(t, http.MethodGet, srv.URL+"/api/projects/missing/metrics/history", nil, alice)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown: %d", resp.StatusCode)
	}
}
