package main

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"traffic-center/domain"
	dbsqlite "traffic-center/internal/sqlite"
)

func TestExtractAnonymizedCase(t *testing.T) {
	db, err := dbsqlite.Open(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err = db.Exec(`INSERT INTO projects(id,name,api_key_hash) VALUES('private-project','private name','hash')`); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	for i := 0; i < 14; i++ {
		ts := now.Add(time.Duration(i-15) * time.Minute).UnixMilli()
		_, err = db.Exec(`INSERT INTO metric_snapshots(project_id,ts,window_s,rps,p50,p95,p99,error_rate,unique_ips,top_ip_rps,avg_payload_bytes,endpoints_json) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, "private-project", ts, 60, 2, 10, 20, 30, 1, 2, 1, 20, `{"/private-path":5}`)
		if err != nil {
			t.Fatal(err)
		}
	}
	_, err = db.Exec(`INSERT INTO metric_snapshots(project_id,ts,window_s,rps,p50,p95,p99,error_rate,unique_ips,top_ip_rps,avg_payload_bytes,endpoints_json) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, "private-project", now.UnixMilli(), 60, 4, 10, 200, 300, 15, 2, 1, 20, `{"/private-path":5}`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO anomalies(project_id,type,severity,message,value,threshold,analysis,detected_at) VALUES(?,?,?,?,?,?,?,?)`, "private-project", "high_latency", "critical", "private message", 200, 100, "private diagnosis", now.UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO anomalies(project_id,type,severity,message,value,threshold,analysis,detected_at) VALUES(?,?,?,?,?,?,?,?)`, "private-project", "/private-path", "high", "private message", 1, 1, "", now.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	cases, err := extract(context.Background(), db, 1)
	if err != nil || len(cases) != 1 {
		t.Fatalf("cases=%+v err=%v", cases, err)
	}
	c := cases[0]
	if c.Expected != nil || c.Evidence.Baseline == nil || *c.Evidence.RequestCount != 240 || c.Evidence.P95 != 200 {
		t.Fatalf("wrong evidence: %+v", c)
	}
	encoded, _ := json.Marshal(c.Evidence)
	for _, secret := range []string{"private-project", "private-path", "private message", "private diagnosis"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("leaked %s: %s", secret, encoded)
		}
	}
	if c.ID != fmt.Sprintf("anomaly-%d", 1) {
		t.Fatal(c.ID)
	}
}

type fakeAnalyst struct{}

func (fakeAnalyst) Diagnose(context.Context, domain.IncidentEvidence) (domain.AnalysisResult, error) {
	return domain.AnalysisResult{Pattern: "error_burst", Direction: "failing_endpoints", EvidenceProbability: 0.9}, nil
}
func TestLabeledScoringUsesProductGate(t *testing.T) {
	n := 40
	evidence := domain.IncidentEvidence{RequestCount: &n, Baseline: &domain.BaselineEvidence{}, Endpoints: &domain.EndpointEvidence{MostErrors: 3}}
	cases := []testCase{{ID: "a", Evidence: evidence, Expected: &label{Patterns: []string{"error_burst"}, Directions: []string{"latency"}}}, {ID: "unlabeled"}}
	rep := runCases(context.Background(), cases, fakeAnalyst{})
	if rep.Calls != 2 || rep.PatternLabeled != 1 || rep.PatternCorrect != 1 || rep.DirectionLabeled != 1 || rep.DirectionCorrect != 0 || rep.Results[0].EvidenceSufficient == nil || !*rep.Results[0].EvidenceSufficient || rep.Results[1].PatternMatch != nil || *rep.Results[1].EvidenceSufficient {
		t.Fatalf("bad scoring: %+v", rep)
	}
}
func TestRequiresCallBudget(t *testing.T) {
	if err := evaluate([]string{"-cases", "input.json"}); err == nil {
		t.Fatal("unbounded evaluation allowed")
	}
}
