// incident-eval exports anonymous historic evidence and evaluates manually
// labeled cases. It never sends project IDs, paths or IP addresses to Gemini.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"time"

	_ "modernc.org/sqlite"
	"traffic-center/domain"
	"traffic-center/internal/gemini"
)

type label struct {
	Patterns   []string `json:"patterns"`
	Directions []string `json:"directions"`
}
type testCase struct {
	ID       string                  `json:"id"`
	Evidence domain.IncidentEvidence `json:"evidence"`
	Expected *label                  `json:"expected,omitempty"`
}
type result struct {
	ID                  string  `json:"id"`
	Pattern             string  `json:"pattern,omitempty"`
	Direction           string  `json:"direction,omitempty"`
	EvidenceProbability float64 `json:"evidence_probability,omitempty"`
	EvidenceSufficient  *bool   `json:"evidence_sufficient,omitempty"`
	InputTokens         int     `json:"input_tokens,omitempty"`
	PatternMatch        *bool   `json:"pattern_match,omitempty"`
	DirectionMatch      *bool   `json:"direction_match,omitempty"`
	Error               string  `json:"error,omitempty"`
}
type report struct {
	Cases            int      `json:"cases"`
	Calls            int      `json:"calls"`
	Errors           int      `json:"errors"`
	PatternLabeled   int      `json:"pattern_labeled"`
	PatternCorrect   int      `json:"pattern_correct"`
	DirectionLabeled int      `json:"direction_labeled"`
	DirectionCorrect int      `json:"direction_correct"`
	Results          []result `json:"results"`
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: incident-eval export|run [flags]")
	}
	switch args[0] {
	case "export":
		return export(args[1:])
	case "run":
		return evaluate(args[1:])
	default:
		return errors.New("usage: incident-eval export|run [flags]")
	}
}
func export(args []string) error {
	f := flag.NewFlagSet("export", flag.ContinueOnError)
	path := f.String("db", "", "existing SQLite database")
	out := f.String("out", "", "new output file (private JSON)")
	per := f.Int("per-type", 1, "cases per alert type (maximum 10)")
	if err := f.Parse(args); err != nil {
		return err
	}
	if *path == "" || *out == "" || *per < 1 || *per > 10 {
		return errors.New("export requires -db and -out and per-type in 1..10")
	}
	abs, err := filepath.Abs(*path)
	if err != nil {
		return err
	}
	u := url.URL{Scheme: "file", Path: abs}
	q := u.Query()
	q.Set("mode", "ro")
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(2)
	cases, err := extract(context.Background(), db, *per)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(cases, "", "  ")
	if err != nil {
		return err
	}
	file, err := os.OpenFile(*out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(append(data, '\n'))
	closeErr := file.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return err
	}
	fmt.Printf("exported %d unlabeled cases; review and label before measuring accuracy\n", len(cases))
	return nil
}
func extract(ctx context.Context, db *sql.DB, per int) ([]testCase, error) {
	rows, err := db.QueryContext(ctx, `SELECT a.id,a.project_id,a.type,a.severity,a.value,a.threshold,a.detected_at,
 s.ts,s.window_s,s.rps,s.p50,s.p95,s.p99,s.error_rate,s.unique_ips,s.top_ip_rps
 FROM anomalies a JOIN metric_snapshots s ON s.id=(SELECT x.id FROM metric_snapshots x
 WHERE x.project_id=a.project_id AND x.ts BETWEEN a.detected_at-10000 AND a.detected_at+10000
 ORDER BY abs(x.ts-a.detected_at),x.id DESC LIMIT 1)
 WHERE a.type IN ('high_traffic','high_error_rate','high_latency','possible_ip_flood') AND a.severity IN ('high','critical')
 ORDER BY a.detected_at DESC LIMIT 5000`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	cases := []testCase{}
	counts := map[string]int{}
	used := map[string]bool{}
	for rows.Next() {
		var id, detected, observed int64
		var project string
		var e domain.IncidentEvidence
		if err := rows.Scan(&id, &project, &e.TriggerType, &e.Severity, &e.Value, &e.Threshold, &detected, &observed, &e.WindowSeconds, &e.RPS, &e.P50, &e.P95, &e.P99, &e.ErrorRate, &e.UniqueIPs, &e.TopIPRPS); err != nil {
			return nil, err
		}
		key := fmt.Sprintf("%s/%d", project, detected)
		if counts[e.TriggerType] >= per || used[key] {
			continue
		}
		counts[e.TriggerType]++
		used[key] = true
		at := time.UnixMilli(observed).UTC()
		e.ObservedAt = &at
		n := int(math.Round(e.RPS * float64(e.WindowSeconds)))
		if n >= 0 {
			e.RequestCount = &n
		}
		e.Baseline = baseline(ctx, db, project, at, e.WindowSeconds)
		cases = append(cases, testCase{ID: fmt.Sprintf("anomaly-%d", id), Evidence: e})
	}
	return cases, rows.Err()
}
func baseline(ctx context.Context, db *sql.DB, project string, at time.Time, window int) *domain.BaselineEvidence {
	gap := max(time.Minute, time.Duration(window)*time.Second)
	rows, err := db.QueryContext(ctx, `SELECT ts,rps,p95,error_rate FROM metric_snapshots WHERE project_id=? AND ts>=? AND ts<=? ORDER BY ts LIMIT 1000`, project, at.Add(-time.Hour).UnixMilli(), at.Add(-gap).UnixMilli())
	if err != nil {
		return nil
	}
	defer func() { _ = rows.Close() }()
	var start, end int64
	var r, p, e []float64
	for rows.Next() {
		var ts int64
		var a, b, c float64
		if rows.Scan(&ts, &a, &b, &c) != nil {
			return nil
		}
		if len(r) == 0 {
			start = ts
		}
		end = ts
		r = append(r, a)
		p = append(p, b)
		e = append(e, c)
	}
	if rows.Err() != nil || len(r) < 12 || end-start < int64((10*time.Minute)/time.Millisecond) {
		return nil
	}
	sort.Float64s(r)
	sort.Float64s(p)
	sort.Float64s(e)
	return &domain.BaselineEvidence{Samples: len(r), Start: time.UnixMilli(start).UTC(), End: time.UnixMilli(end).UTC(), RPS: r[len(r)/2], P95: p[len(p)/2], ErrorRate: e[len(e)/2]}
}
func evaluate(args []string) error {
	f := flag.NewFlagSet("run", flag.ContinueOnError)
	path := f.String("cases", "", "JSON cases file")
	maxCalls := f.Int("max-calls", 0, "maximum paid provider calls, required")
	model := f.String("model", gemini.DefaultModel, "Gemini Decisions model")
	if err := f.Parse(args); err != nil {
		return err
	}
	if *path == "" || *maxCalls < 1 || *maxCalls > 100 {
		return errors.New("run requires -cases and -max-calls between 1 and 100")
	}
	if *model != gemini.DefaultModel {
		return errors.New("unsupported model")
	}
	key := os.Getenv("GEMINI_API_KEY")
	if key == "" {
		return errors.New("GEMINI_API_KEY is required")
	}
	file, err := os.Open(*path)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if info.Size() > 10<<20 {
		return errors.New("cases file exceeds 10 MiB")
	}
	dec := json.NewDecoder(io.LimitReader(file, 10<<20))
	dec.DisallowUnknownFields()
	var cases []testCase
	if err := dec.Decode(&cases); err != nil {
		return fmt.Errorf("decode cases: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("cases file must contain exactly one JSON array")
	}
	if len(cases) == 0 || len(cases) > *maxCalls {
		return fmt.Errorf("case count must be 1..max-calls")
	}
	ids := map[string]bool{}
	for _, c := range cases {
		if c.ID == "" || ids[c.ID] {
			return errors.New("case IDs must be nonempty and unique")
		}
		ids[c.ID] = true
		if !contains([]string{"high_traffic", "high_error_rate", "high_latency", "possible_ip_flood"}, c.Evidence.TriggerType) || !contains([]string{"high", "critical"}, c.Evidence.Severity) {
			return errors.New("case evidence has an invalid alert type or severity")
		}
		if c.Expected != nil {
			for _, v := range c.Expected.Patterns {
				if !contains([]string{"traffic_surge", "error_burst", "latency_degradation", "mixed", "unclear"}, v) {
					return errors.New("invalid expected pattern")
				}
			}
			for _, v := range c.Expected.Directions {
				if !contains([]string{"traffic_sources", "failing_endpoints", "latency", "more_evidence"}, v) {
					return errors.New("invalid expected direction")
				}
			}
		}
	}
	client := gemini.New(key, *model)
	rep := runCases(context.Background(), cases, client)
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(rep); err != nil {
		return err
	}
	if rep.Errors > 0 {
		return errors.New("one or more provider calls failed")
	}
	return nil
}

func runCases(ctx context.Context, cases []testCase, client domain.Analyst) report {
	rep := report{Cases: len(cases), Results: make([]result, 0, len(cases))}
	for _, c := range cases {
		rep.Calls++
		response, err := client.Diagnose(ctx, c.Evidence)
		r := result{ID: c.ID}
		if err != nil {
			rep.Errors++
			r.Error = err.Error()
		} else {
			r.Pattern = response.Pattern
			r.Direction = response.Direction
			r.EvidenceProbability = response.EvidenceProbability
			sufficient := domain.EvidenceSufficient(c.Evidence, response)
			r.EvidenceSufficient = &sufficient
			r.InputTokens = response.InputTokens
			if c.Expected != nil && len(c.Expected.Patterns) > 0 {
				m := contains(c.Expected.Patterns, r.Pattern)
				r.PatternMatch = &m
				rep.PatternLabeled++
				if m {
					rep.PatternCorrect++
				}
			}
			if c.Expected != nil && len(c.Expected.Directions) > 0 {
				m := contains(c.Expected.Directions, r.Direction)
				r.DirectionMatch = &m
				rep.DirectionLabeled++
				if m {
					rep.DirectionCorrect++
				}
			}
		}
		rep.Results = append(rep.Results, r)
	}
	return rep
}
func contains(a []string, s string) bool {
	for _, v := range a {
		if v == s {
			return true
		}
	}
	return false
}
