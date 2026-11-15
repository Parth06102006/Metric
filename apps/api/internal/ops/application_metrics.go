package ops

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"time"
)

// applicationMetrics emits aggregate-only operational signals. A failed query
// is explicit and is not indistinguishable from an empty queue.
func (s *Service) applicationMetrics(w http.ResponseWriter, request *http.Request) {
	ctx, cancel := context.WithTimeout(request.Context(), 2*time.Second)
	defer cancel()
	ok, pending, oldest, calls := 0, 0, int64(0), 0
	db, err := readOnlyDB(s.Database)
	if err == nil {
		var oldestMs sql.NullInt64
		err = db.QueryRowContext(ctx, `SELECT count(*),min(created_at) FROM analysis_jobs WHERE status IN ('pending','processing','dispatching')`).Scan(&pending, &oldestMs)
		if err == nil {
			err = db.QueryRowContext(ctx, `SELECT COALESCE((SELECT reserved FROM analysis_daily_calls WHERE utc_day=?),0)`, time.Now().UTC().Format("2006-01-02")).Scan(&calls)
		}
		if err == nil {
			ok = 1
			if oldestMs.Valid {
				oldest = oldestMs.Int64 / 1000
			}
		}
		_ = db.Close()
	}
	_, _ = fmt.Fprintf(w, `# HELP metric_analysis_queue_stat_success Whether analysis queue and call budget could be read.
# TYPE metric_analysis_queue_stat_success gauge
metric_analysis_queue_stat_success %d
# HELP metric_analysis_pending_jobs Analysis jobs waiting or in progress.
# TYPE metric_analysis_pending_jobs gauge
metric_analysis_pending_jobs %d
# HELP metric_analysis_oldest_job_timestamp_seconds Oldest queued job creation time, or zero when empty.
# TYPE metric_analysis_oldest_job_timestamp_seconds gauge
metric_analysis_oldest_job_timestamp_seconds %d
# HELP metric_analysis_calls_reserved_today Provider calls reserved in the current UTC day.
# TYPE metric_analysis_calls_reserved_today gauge
metric_analysis_calls_reserved_today %d
`, ok, pending, oldest, calls)
	if s.APIReadyURL == "" {
		return
	}
	probeOK, ready := 0, 0
	probeCtx, probeCancel := context.WithTimeout(request.Context(), 2*time.Second)
	defer probeCancel()
	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, s.APIReadyURL, nil)
	if err == nil {
		client := &http.Client{Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		resp, err := client.Do(req)
		if err == nil {
			probeOK = 1
			if resp.StatusCode == http.StatusOK {
				ready = 1
			}
			_ = resp.Body.Close()
		}
	}
	_, _ = fmt.Fprintf(w, `# HELP metric_api_probe_success Whether the API readiness endpoint was reachable.
# TYPE metric_api_probe_success gauge
metric_api_probe_success %d
# HELP metric_api_ready Whether the API readiness endpoint returned 200.
# TYPE metric_api_ready gauge
metric_api_ready %d
`, probeOK, ready)
}
