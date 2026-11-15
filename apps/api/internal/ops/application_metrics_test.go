package ops

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestApplicationMetrics(t *testing.T) {
	dir := t.TempDir()
	path, db := liveDB(t, dir)
	now := time.Now().UTC()
	if _, err := db.Exec(`INSERT INTO anomalies(id,project_id,type,severity,message,value,threshold,detected_at) VALUES(1,'p','x','high','rule',1,1,?)`, now.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO analysis_jobs(anomaly_id,project_id,state_json,status,created_at) VALUES(1,'p','{}','pending',?)`, now.Add(-time.Minute).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO analysis_daily_calls(utc_day,reserved) VALUES(?,2)`, now.Format("2006-01-02")); err != nil {
		t.Fatal(err)
	}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }))
	defer api.Close()
	service := NewService(path, dir, nil, nil)
	service.APIReadyURL = api.URL
	w := httptest.NewRecorder()
	service.Metrics(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := w.Body.String()
	for _, expected := range []string{"metric_analysis_queue_stat_success 1", "metric_analysis_pending_jobs 1", "metric_analysis_calls_reserved_today 2", "metric_api_probe_success 1", "metric_api_ready 0", fmt.Sprintf("metric_analysis_oldest_job_timestamp_seconds %d", now.Add(-time.Minute).Unix())} {
		if !strings.Contains(body, expected) {
			t.Fatalf("missing %q: %s", expected, body)
		}
	}
	api.Close()
	w = httptest.NewRecorder()
	service.Metrics(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(w.Body.String(), "metric_api_probe_success 0") {
		t.Fatal("unreachable API not reported")
	}
}
