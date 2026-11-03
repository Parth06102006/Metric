# Replacing Gemini with Jev (TypeSafe) — Architecture Plan

Status: proposal, not implemented.
Scope: `apps/api` (Go), `apps/web` (React), SQLite schema, config, docs.
Target: read this top to bottom before touching code.

---

## 0. TL;DR

Jev is **not** a text generator. It is a "System One" decision model: you send one
`state` (the facts) plus N typed questions (`noul` = yes/no probability, `choice` =
pick one label, `score` = position on an ordered scale), and you get **typed answers
with probabilities and confidence**. It does not write sentences and does not explain
itself.

So the correct migration is not "swap the HTTP call". It is:

1. Keep the **existing rule engine** (`domain.Detect`) as the source of truth for
   *that* an anomaly happened. Never let the model decide thresholds.
2. Add a **decision layer** (`domain.Analyst`) that asks Jev a fixed, atomic question
   set about the incident context, and returns a typed `Diagnosis`
   (pattern, primary signal, recommended action, urgency, confidence, evidence-sufficiency).
3. **Compose the human sentence in Go** from those labels + the numeric facts
   (deterministic, testable, no hallucination).
4. Gate on confidence: low-confidence or insufficient-evidence answers fall back to
   today's rule-based text and are stored as `unverified`.
5. Feed Jev a **compact, redacted context pack**, not "all the metrics". The single
   highest-value addition is a **baseline** (last 60 min) — without it, no model can
   tell "spike" from "normal".

Everything else (files, schema, tests, rollout, MCP tooling) is below.

---

## 1. What Jev actually gives us (verified against docs)

Endpoint: `POST https://api.typesafe.ai/v1/systemone`
Auth: `Authorization: Bearer <TYPESAFE_API_KEY>`
Body: `{ model, state, questions }` — `state` is a string, object, or array.

| Primitive | Question | Answer |
| --- | --- | --- |
| `noul` | yes/no (probability of yes) | `{ "type":"noul", "noul": 0.93 }` |
| `choice` | pick one of ≤255 labels | `{ "type":"choice", "choice":"latency_degradation", "probabilities":{...}, "confidence":0.71 }` |
| `score` | ordered levels (2–10) | `{ "type":"score", "score":1.6, "legend":{...}, "probabilities":{...}, "confidence":0.62 }` |

Facts that shaped this design:

- **Questions run in parallel inside one request.** Asking 8 narrow questions costs
  one round trip, not eight. Fan out, compose in code.
- **Noul has no `confidence` field.** Derive it: `confidence = |2p − 1|`
  (docs' own formula). Thresholds they recommend: act on `p ≥ 0.7`, treat `p ≤ 0.3`
  as no.
- **Accuracy degrades as `state` grows** (documented "jaggedness"). Keep the pack
  small (~600–1,200 tokens), not "everything".
- **Context limits:** 64k per request / 32k per question (default). We should be
  50× under that.
- **~100 ms typical latency**, no streaming, no retries built in for raw HTTP
  (429/529 → exponential backoff; the SDKs do this, a Go client must).
- **Pricing:** $42 per billion input tokens, output free. Our worst case (4 anomaly
  types / 60 s dedupe / project, ~1k tokens) is ~$0.00004 per call — negligible.
- **No Go SDK exists** (JS + Python only). See §3 for the two options.
- **Output is advisory text/data**, never an action. Code owns side effects.

Implication in one line: **Jev replaces the *judgement*, not the *paragraph*. The
paragraph becomes a template.**

---

## 2. Current state (what we are changing)

```text
anomaly.Worker (every 5 s)
  └─ anomaly.Usecase.Evaluate()
       ├─ domain.Detect(snapshot, thresholds)      // 4 fixed rules
       ├─ HasRecent(type, dedupe)                  // 1 alert/type/60 s
       ├─ llm.Client.Explain(...)                  // Gemini, 2 sentences, 4 s timeout
       ├─ AnomalyRepository.Save()                 // analyses stored as TEXT
       └─ Hub.BroadcastAnomaly()
```

| Concern | Today |
| --- | --- |
| Provider code | `apps/api/internal/llm/client.go` (raw Gemini REST, no retry, no breaker) |
| Port | `domain.Explainer.Explain(ctx, projectID, anomaly, snapshot) string` |
| Storage | `anomalies.analysis TEXT` (free prose, unstructured) |
| Context sent | 5 numbers + anomaly type (no baseline, no endpoints, no history) |
| UI | `<p>{anomaly.analysis}</p>` in `ProjectDetail.tsx` |
| Config | `GEMINI_API_KEY`, `GEMINI_MODEL` |
| Failure mode | any error → deterministic `fallback()` text |

Weaknesses this plan fixes: no baseline (can't distinguish spike vs normal), no
structured output (can't filter/aggregate/route on it), no confidence (can't tell
"guess" from "certain"), blocking 4 s call in the 5 s tick, provider hard-wired to
the usecase through a string-typed port.

---

## 3. Decision: where does Jev run?

| Option | Verdict |
| --- | --- |
| **A. Go HTTP client inside `apps/api`** (new `internal/jev`) | **Recommended.** No new runtime/service/port, key stays server-side, fits the existing clean-architecture adapters, one deploy unit, ~250 LOC of typed client + contract tests. We lose the official SDK's retry/zstd/image helpers — all of which we can implement or don't need. |
| B. TypeScript sidecar (`apps/ai`) using `@typesafe-ai/sdk` | Not now. Real gains only if we needed images, zstd compression, or SDK-managed retries. Costs: new container, new healthcheck, new failure mode, IPC latency, compose + Dockerfile + CI changes for one endpoint. The port design in §5 lets this slot in later without touching usecases. |
| C. Call Jev from the SPA | No. Leaks the API key to the browser, breaks the single-origin/self-hosted trust model, and CORS. |
| D. Hybrid: Jev decides + Gemini writes prose | No, unless prose quality becomes a requirement. Two vendors, two keys, two failure modes, and the prose is not the part that needs intelligence. |

**Rule of thumb:** the Go client is a thin, fully-typed transport. All product logic
(which questions, what the answers mean, what text to show) lives outside it.

---

## 4. Target architecture

```text
anomaly.Worker (5 s tick)
  │
  ├─ metrics.Usecase.Current() ──────────────► Snapshot
  ├─ NEW metrics.Usecase.Baseline(project) ──► Baseline   (SnapshotRepository.Range)
  ├─ NEW domain.Summarize(records) ──────────► EndpointSummary[]  (from live window)
  │
  └─ anomaly.Usecase.Evaluate()
       ├─ domain.Detect()                ← unchanged, deterministic, authoritative
       ├─ analyzer.Diagnose(IncidentContext)     ← NEW decision layer
       │     ├─ build state pack (redacted, ≤1.2k tokens)
       │     ├─ one POST /v1/systemone with 8 atomic questions
       │     ├─ domain.Interpret(answers)  ← confidence gates → Diagnosis
       │     └─ on timeout/429/5xx/low-confidence → domain.FallbackDiagnosis()
       ├─ domain.Narrative(diagnosis, ctx)  ← deterministic sentence, no model
       ├─ AnomalyRepository.Save()          ← prose + structured diagnosis
       └─ Hub.BroadcastAnomaly()
```

```text
        ┌───────────────────────────────┐
        │ apps/api                      │
        │                               │
  ctx ──►  domain (pure)               │
        │   IncidentContext             │
        │   Diagnosis + gates           │
        │   Narrative templates         │
        │           ▲                   │
        │           │ Diagnosis         │
        │  internal/jev (adapter) ──────┼──► api.typesafe.ai
        │   client.go  (HTTP, typed)    │      POST /v1/systemone
        │   questions.go (catalog)      │
        │   interpret.go (map answers)  │
        └───────────────────────────────┘
```

Dependency rule preserved: `domain` stays stdlib-only and provider-agnostic;
`internal/jev` implements the port; `anomaly` depends only on the port.

---

## 5. Port + domain types

### 5.1 New port (`domain/ports.go`)

```go
// Analyst turns one incident context into a typed diagnosis.
// Implementations must not mutate the context or take longer than their
// configured timeout; a returned error means "no usable judgement".
type Analyst interface {
	Diagnose(ctx context.Context, incident IncidentContext) (Diagnosis, error)
}
```

`Explainer` is deleted. `anomaly.Usecase` takes an `Analyst` (nil-safe, as today).

### 5.2 New domain data (`apps/api/domain/incident.go`)

```go
type IncidentContext struct {
	Project     ProjectRef
	Trigger     Anomaly            // the rule that fired
	Snapshot    Snapshot
	Baseline    Baseline           // zero value = unavailable
	Thresholds  Threshold
	Endpoints   []EndpointSummary  // top 5
	Sources     SourceSummary      // counts/share only, no raw IPs
	History     []AnomalySummary   // last 5, type + age only
}

type Diagnosis struct {
	Pattern       string   // enum, see §6
	PrimarySignal string   // rps|p95|p99|error_rate|top_ip_share|unknown
	Action        string   // enum, see §6
	Urgency       float64  // 0..2 weighted score
	Confidence    float64  // min of the gating answers
	EvidenceOK    bool
	NeedsHuman    bool
	Status        string   // verified | unverified | insufficient_evidence | disabled | error
	Model         string   // versioned ID returned by the API, e.g. jev-1.14.0
	Answers       map[string]float64 // raw noul probabilities + chosen confidences, for audit
}
```

`Anomaly` gains `Diagnosis *Diagnosis \`json:"diagnosis,omitempty"\`` and keeps
`Analysis string` (composed text) so every existing consumer keeps working.

### 5.3 Baseline (`apps/api/metrics/baseline.go` + `Usecase.Baseline`)

Pure `ComputeBaseline(samples []Snapshot, current Snapshot) Baseline` returning
medians and ratios over a 60-min lookback:

```go
type Baseline struct {
	Minutes        int
	Samples        int
	RPS            float64
	P95            float64
	ErrorRate      float64
	RPSRatio       float64 // current/baseline
	P95Ratio       float64
	ErrorRateDelta float64 // percentage points
}
```

This is the difference between "P95 = 812 ms" (meaningless) and "P95 is 8.4× its
1-hour median" (a question Jev can actually answer). `SnapshotRepository.Range`
already exists and is unused — no schema change.

### 5.4 Endpoint summary (`apps/api/domain/summary.go`)

Pure `Summarize(records []LogRecord, top int) []EndpointSummary` computed from the
**already-in-memory** live window (`RecordRepository.Recent`), so no schema or API
change is required:

```go
type EndpointSummary struct {
	Path      string  // redacted form
	Requests  int
	Share     float64
	Errors5xx int
	ErrorRate float64
	P95Ms     float64
}
```

This gives Jev the "errors are concentrated on `/checkout`" signal that a single
aggregate `error_rate` cannot express.

---

## 6. The questions (this is the part you asked about)

One request, 8 atomic questions, all over the same `state`, evaluated in parallel.
Names are stable identifiers; answers are looked up by name.

| # | Name | Type | Question (short form) | How code uses it |
| --- | --- | --- | --- | --- |
| 1 | `pattern` | choice | Which incident pattern best explains `trigger` given `window` and `baseline`? | Narrative headline; `insufficient_evidence` → `unverified` |
| 2 | `primary_signal` | choice | Which measurement is most responsible for `trigger`? | "Driven by **P95 latency** (8.4× baseline)." |
| 3 | `action` | choice | Which operator action does this evidence support? | Recommendation line; never executed automatically |
| 4 | `urgency` | score | How urgent is this, from monitor → investigate today → act now? | `score ≥ 1.5 && conf ≥ 0.6` → escalation badge |
| 5 | `is_single_source` | noul | Does one client source account for most of `window.top_ip_share`? | Chooses `rate_limit_source` hint; sanity-checks IP-flood rule |
| 6 | `errors_are_concentrated` | noul | Are 5xx errors concentrated in one or two endpoints? | Points the operator at the endpoint, not the whole API |
| 7 | `evidence_sufficient` | noul | Does `state` contain enough evidence to support `action`? | `p < 0.3` → `insufficient_evidence`, rule text wins |
| 8 | `needs_human` | noul | Would acting on this automatically risk a wrong or harmful mitigation? | `p ≥ 0.7` → "review before acting", destructive hints suppressed |

### 6.1 Exact labels (compile-time constants in Go)

- `pattern`: `traffic_surge`, `single_source_abuse`, `error_burst`,
  `latency_degradation`, `dependency_outage`, `deploy_regression`,
  `benign_variance`, `insufficient_evidence`
- `primary_signal`: `rps`, `p95`, `p99`, `error_rate`, `top_ip_share`, `unknown`
- `action`: `scale_out`, `rate_limit_source`, `rollback_deploy`,
  `inspect_dependency`, `raise_threshold`, `no_action`, `page_oncall`
- `urgency` levels: `["monitor only", "investigate within the hour", "act now"]`

Every label is a Go const; the client **validates the returned label against the
const set** and treats an unknown label as an error (defensive against model/version
drift). That is the practical meaning of "type-safe AI" here: closed sets on both
sides, checked at the boundary, not free text.

### 6.2 Criteria discipline

Each `choice` option gets contrastive criteria (`what`, `not_for`, `examples`), and
each `noul` gets `true`/`false` criteria — this is what stops `latency_degradation`
from absorbing `dependency_outage`. Written once, in `internal/jev/questions.go`, as
structured Go data (not string concatenation).

### 6.3 How answers become a Diagnosis (gating, `domain/interpret.go`)

```text
evidence = noul(evidence_sufficient)
if evidence < 0.30                      -> Status=insufficient_evidence, use fallback text
if pattern == insufficient_evidence     -> Status=unverified,               use fallback text
conf = min(choice.confidence(pattern), choice.confidence(action))
if conf < JEV_MIN_CONFIDENCE (0.55)     -> Status=unverified,               keep Jev labels but mark them
else                                    -> Status=verified
urgency  = score(urgency)               // may be between levels
needsHuman = noul(needs_human) >= 0.70
```

Severity is **never** silently rewritten: the rule engine's severity stays the
alerting severity; the diagnosis adds `urgency` and `needs_human` as advisory
fields. This keeps the audit story clean (rule → severity; model → advice).

### 6.4 Narrative (deterministic, `domain/narrative.go`)

```text
"Latency degradation (71% confidence). P95 is 812 ms, 8.4× the 1-hour median;
 errors are concentrated on /checkout (11.7% of 1,200 requests).
 Suggested: inspect upstream dependency. Urgency: act now."
```

Unverified: `"Latency degradation (unverified, 44% confidence). …"`
Insufficient evidence: `"Rule-based analysis: …"` (today's text, unchanged).

Template + golden-file tests. No model output is ever rendered as HTML.

---

## 7. Context contract (the "state pack")

Send **one object**, named fields, numbers rounded to 2 dp, arrays capped. This is
what the model sees — nothing else.

```json
{
  "project": { "name": "checkout", "window_s": 60 },
  "trigger": { "rule": "high_latency", "severity": "high", "value": 812.44, "threshold": 500 },
  "window": {
    "rps": 41.2, "p50": 88.1, "p95": 812.44, "p99": 1203.2,
    "error_rate": 3.12, "unique_ips": 210, "top_ip_rps": 18.4,
    "avg_payload_bytes": 640
  },
  "baseline": {
    "minutes": 60, "samples": 720, "rps": 38.9, "p95": 96.4,
    "error_rate": 0.42, "rps_ratio": 1.06, "p95_ratio": 8.43,
    "error_rate_delta_pp": 2.7
  },
  "thresholds": { "p95_ms": 500, "error_rate": 5, "rps_max": 100, "req_per_ip_max": 30 },
  "endpoints": [
    { "path": "/checkout", "requests": 1200, "share": 0.48, "errors_5xx": 140,
      "error_rate": 11.67, "p95_ms": 1450 }
  ],
  "sources": { "unique_ips": 210, "top_ip_share": 0.45, "top_ip_rps": 18.4 },
  "history": [ { "rule": "high_error_rate", "minutes_ago": 22, "severity": "critical" } ]
}
```

Rules:

- **Do not send raw IPs.** Only counts/shares (`top_ip_share`). IPs are personal data
  and the state crosses a third-party boundary.
- **Normalize endpoint paths** before sending (`/users/8f3a…/orders` → `/users/:id/orders`,
  regex for UUID/hex/long digits). Add `JEV_REDACT=true` default on.
- **Cap:** top 5 endpoints, last 5 anomaly types, no raw log lines, no payloads,
  no headers, no query strings.
- **Project name** is the only free text we send; if that's sensitive, send the
  project ID prefix instead (`JEV_SEND_PROJECT_NAME=false`).
- **Budget:** if the marshalled pack exceeds `JEV_MAX_STATE_BYTES` (default 8192),
  drop `history` first, then `endpoints` beyond top 3.
- **Do not send the API key, tokens, or full URLs.**

Why not "all the metrics": accuracy degrades with state size, cost scales with input
tokens, and irrelevant fields create false correlations. Trigger + window + baseline
+ endpoint concentration + history is enough to answer the 8 questions above.

---

## 8. Runtime behaviour

| Concern | Decision |
| --- | --- |
| Where | Inline in `anomaly.Usecase.Evaluate`, after `Detect`, before `Save` — dedupe already limits this to ≤1 call/type/60 s/project |
| Timeout | `JEV_TIMEOUT=1500ms` (p99 of a ~100 ms API), context cancelled with the tick |
| Concurrency | buffered `semaphore(4)` shared by the client so a multi-project tick can't open 50 sockets |
| Retry | 429/529/5xx: 2 retries, backoff 200 ms → 800 ms, honour `Retry-After`; never retry 4xx |
| Circuit breaker | 5 consecutive failures → open 60 s → skip calls, serve fallback, log once at WARN |
| Budget | `JEV_MAX_CALLS_PER_HOUR` (default 120, global) — protects against a pathological project |
| Caching | none needed: dedupe is the cache; same `(project, rule)` won't re-call within 60 s |
| Failure | any error → `FallbackDiagnosis` (status `error`), `Narrative` renders today's rule text; the anomaly is still saved and broadcast |
| Observability | one structured log per call: `project_id, rule, model, latency_ms, input_tokens, confidence, status, cached=false`; counter-style log on breaker open/skip |
| Kill switch | `JEV_MODE=off` → no outbound calls at all (default until a key is configured) |

No queue in v1. A queue would decouple a slow provider from the 5 s tick, but the
timeout + breaker already bound the worst case (≤1.5 s per anomaly, at most a few
per tick). Revisit if p95 call latency exceeds the tick.

---

## 9. Files affected (complete list)

### Backend — new

| Path | Purpose |
| --- | --- |
| `apps/api/domain/incident.go` | `IncidentContext`, `Baseline`, `EndpointSummary`, `SourceSummary`, label consts |
| `apps/api/domain/diagnosis.go` | `Diagnosis`, status consts, `Interpret(answers)`, confidence gates |
| `apps/api/domain/narrative.go` | deterministic prose from `Diagnosis` + context |
| `apps/api/domain/summary.go` | `Summarize(records, top)` pure function |
| `apps/api/domain/incident_test.go`, `diagnosis_test.go`, `summary_test.go` | table tests |
| `apps/api/internal/jev/client.go` | HTTP transport: auth, typed request/response, retry, breaker, semaphore |
| `apps/api/internal/jev/questions.go` | question catalog (labels + criteria) as typed Go data |
| `apps/api/internal/jev/adapter.go` | `Diagnose(ctx, IncidentContext) (domain.Diagnosis, error)`, state-pack builder + redaction |
| `apps/api/internal/jev/client_test.go` | `httptest` fake: 200/401/422/429→retry/529/timeout/malformed/unknown-label |
| `apps/api/internal/jev/adapter_test.go` | state-pack golden JSON, redaction, budget truncation |
| `apps/api/internal/jev/testdata/state_high_latency.json` | golden pack |
| `apps/api/internal/jev/testdata/incidents.json` | eval fixtures (synthetic incidents + expected pattern) |
| `apps/api/metrics/baseline.go` | `ComputeBaseline` + `Usecase.Baseline` |
| `apps/api/metrics/baseline_test.go` | table tests |

### Backend — modified

| Path | Change |
| --- | --- |
| `apps/api/domain/ports.go` | delete `Explainer`; add `Analyst` |
| `apps/api/domain/anomaly.go` | `Anomaly.Diagnosis *Diagnosis` (additive JSON) |
| `apps/api/anomaly/usecase.go` | take `Analyst`; build `IncidentContext`; interpret; compose narrative; fallback |
| `apps/api/anomaly/usecase_test.go` | new fake analyst; assert verified/unverified/fallback paths |
| `apps/api/anomaly/repository/sqlite/repository.go` | persist/read `diagnosis_json`, `analysis_model`, `analysis_status`, `analysis_confidence` |
| `apps/api/anomaly/repository/memory/repository.go` | mirror new fields |
| `apps/api/anomaly/worker.go` | pass records/baseline into `EvaluateProject` → `Evaluate` |
| `apps/api/metrics/usecase.go` | expose `Baseline`; keep `Current` untouched |
| `apps/api/internal/sqlite/db.go` | migration v3 (see §10), `schemaVersion = 3` |
| `apps/api/internal/sqlite/db_test.go` | v2 → v3 upgrade test |
| `apps/api/internal/config/config.go` | replace `Gemini*` with `Jev*` (§11) |
| `apps/api/internal/config/config_test.go` | parsing + `JEV_MODE` validation + production guard |
| `apps/api/app/main.go` | wire `jev.New(...)` instead of `llm.New(...)` |
| `apps/api/internal/server/server.go` | `Deps` unchanged; (optional Phase 5) `POST /api/projects/{id}/anomalies/{anomalyID}/analyze` |
| `apps/api/internal/server/server_test.go` | anomaly JSON now carries `diagnosis` |
| `apps/api/internal/llm/client.go` | **delete** (and `internal/llm/`) |
| `README.md`, `docs/architecture.md` | flows §3, backend §4, data model §5, tradeoffs §10, config table |
| `.env.example`, `compose.yaml` | env vars; `GEMINI_*` removed |
| `Makefile` | `make jev-eval` (live eval, needs key), keep `make check` green offline |

### Frontend

| Path | Change |
| --- | --- |
| `apps/web/src/types/api.ts` | `diagnosisSchema` (zod): pattern/action/urgency/confidence/status/model; `analysis` stays |
| `apps/web/src/components/DiagnosisCard.tsx` | **new**: pattern + confidence + evidence line + suggested action + "unverified" / "review before acting" states |
| `apps/web/src/routes/ProjectDetail.tsx` | render `DiagnosisCard` inside each anomaly `<li>` when `diagnosis` present; keep plain `analysis` fallback |
| `apps/web/src/components/DiagnosisCard.test.tsx` | **new**: verified / unverified / insufficient-evidence / no-diagnosis |
| `apps/web/src/features/projects/api.ts` | unchanged (response is additive) |

### Deleted

`apps/api/internal/llm/` (client + tests), `GEMINI_API_KEY`, `GEMINI_MODEL`.

---

## 10. Data model (migration v3)

```sql
ALTER TABLE anomalies ADD COLUMN diagnosis_json     TEXT    NOT NULL DEFAULT '';
ALTER TABLE anomalies ADD COLUMN analysis_model     TEXT    NOT NULL DEFAULT '';
ALTER TABLE anomalies ADD COLUMN analysis_status    TEXT    NOT NULL DEFAULT '';
ALTER TABLE anomalies ADD COLUMN analysis_confidence REAL   NOT NULL DEFAULT 0;
PRAGMA user_version = 3;
```

- Additive + idempotent, matching the existing `user_version` pattern; no table
  rebuild, no data copy. Old rows get `analysis_status='rule_based'` on read when
  empty, so history renders exactly as today.
- `diagnosis_json` holds the typed `Diagnosis` (answers map included for audit).
- Why a column instead of only JSON-in-`analysis`: we will want
  `WHERE analysis_status='verified'` and evaluation queries over pattern labels.
- `Analysis` (prose) remains the single field the UI needs for the fallback path.

---

## 11. Config / env

| Variable | Default | Purpose |
| --- | --- | --- |
| `TYPESAFE_API_KEY` | empty | Jev API key. Empty ⇒ `JEV_MODE=off` |
| `JEV_MODE` | `observe` | `off` (no calls) / `observe` (call, log, keep rule text) / `enforce` (persist diagnosis + narrative) |
| `JEV_MODEL` | `jev-1.14.0` | **Pin the versioned ID**, not `jev-latest`, so thresholds don't drift |
| `JEV_BASE_URL` | `https://api.typesafe.ai` | override for tests/proxies |
| `JEV_TIMEOUT` | `1500ms` | per-call deadline |
| `JEV_MIN_CONFIDENCE` | `0.55` | below ⇒ `unverified` |
| `JEV_MAX_CALLS_PER_HOUR` | `120` | global budget |
| `JEV_MAX_STATE_BYTES` | `8192` | pack truncation threshold |
| `JEV_REDACT` | `true` | mask/normalize endpoints, never send IPs |

Production guard (mirrors the existing `PROXY_SHARED_SECRET` rule): if
`APP_ENV=production && JEV_MODE!=off && TYPESAFE_API_KEY==""` → startup error.

`observe` mode is the rollout lever: the full pipeline runs and logs, but stored data
and UI are unchanged. Flip to `enforce` after the eval in §13.

---

## 12. API/UI surface changes

- `GET /api/projects/{id}/anomalies` and the `anomaly_alert` websocket event gain an
  **optional** `diagnosis` object. Additive; old clients ignore it.
- `analysis` remains a string and remains the fallback rendering.
- New UI shows a compact card:
  - badge: pattern (human label) + `verified` / `unverified` / `rule-based`
  - one evidence line (baseline ratio, endpoint concentration)
  - suggested action + urgency
  - model + confidence in a muted line, `title` attribute for raw answers
- No new route needed for v1. "Ask an operator question" is **not** a Jev feature
  (no open-ended answering); if we want it later it is a separate LLM concern.

---

## 13. Testing & evaluation

Offline (part of `make check`, no key needed):

1. `domain.Interpret` table tests: every gate boundary (0.29/0.30, 0.54/0.55, urgency
   1.49/1.50), unknown label, missing answer name, NaN.
2. `domain.Narrative` golden files: verified / unverified / insufficient-evidence /
   no-baseline.
3. `internal/jev` client against `httptest`: auth header, body shape, unknown label
   rejected, 429 retried twice then fallback, 401 no retry, breaker opens after 5
   failures, timeout respected, oversized pack truncated.
4. State-pack golden JSON: asserts redaction (no IPv4/IPv6 literals, no UUIDs in
   paths) and byte budget.
5. `anomaly.Usecase`: analyst returns diagnosis → persisted + broaadcast; analyst
   errors → fallback saved with `status=error`; nil analyst → unchanged behaviour.
6. Migration test v2 schema → v3 columns + `user_version`.
7. Web: `DiagnosisCard` states + `ProjectDetail` still renders without `diagnosis`.

Live (manual, `make jev-eval`, requires key):

- `testdata/incidents.json`: ~30 hand-built synthetic incidents with an expected
  pattern label. Report per-label accuracy, mean confidence, and a confidence-vs-
  accuracy table. **Do not flip `JEV_MODE=enforce` until accuracy on the obvious
  cases (traffic surge vs latency vs error burst) is sane and the low-confidence
  bucket actually correlates with mistakes.**
- `make jev-eval` also prints token usage and estimated cost per 1k anomalies.

---

## 14. Rollout phases

| Phase | Work | Exit criteria |
| --- | --- | --- |
| 0 | This doc + question catalog + state-pack fixture | You approve question set and state contents |
| 1 | `Analyst` port, `Diagnosis`/`Narrative`, delete Gemini, fallback wired | `make check` green, behaviour identical with `JEV_MODE=off` |
| 2 | Baseline + endpoint summaries | Narrative shows ratios; no schema/API change yet |
| 3 | Jev client + adapter, `JEV_MODE=observe` | Logs show labels + confidence in staging |
| 4 | Migration v3 + persistence + API/zod + `DiagnosisCard`; `enforce` | UI shows verified/unverified; rollback = `JEV_MODE=off` |
| 5 | Ops hardening (breaker tuning, budgets, eval report), docs | Documented p95 latency, cost, failure drill |

Rollback at any phase: `JEV_MODE=off` (no code revert), or revert the one-line
`main.go` wiring + restore `internal/llm` from git.

---

## 15. MCP tools — what actually helps here

Honest answer: there is no "architecture MCP". The leverage is **docs-as-truth**
plus the repo, and only two servers are relevant.

| MCP | Use it for | Verdict |
| --- | --- | --- |
| **context7** | TypeSafe/Jev contract, primitives, confidence, patterns. Verified during this plan with `/typesafe-docs/typesafeai` and `/websites/typesafe_ai_sdk_javascript`. | **Use.** Recipe: `resolve-library-id` → `query-docs` per concept ("HTTP request schema", "confidence thresholds", "fan-out"). Prevents guessing the answer JSON. |
| **better-design** | The `DiagnosisCard` UI: `get-ui-principle` before styling, `get-review-rules` + `inspect-spacing` + `check-comprehension` after. Keep the existing Tailwind `ui.tsx` design system — do not introduce shadcn here. | **Use in Phase 4.** |
| **playwright** | Verify the card at 390/1440, all four states, keyboard focus, no layout break in the anomaly list. | **Use in Phase 4.** |
| **rea** | Only for reverse-engineering binaries/pages. Nothing to reverse here. | No. |
| **shadcn / magicui / 21st** | Component sourcing. The project already has its own components; adding a second design system would be a regression. | No. |

Note on other context sources: `docs.typesafe.ai/llms.txt` is a machine-readable index
of every page — cheaper and more complete than general web search. Use it directly
(`curl .../<page>.md`) or through context7.

---

## 16. Risks & mitigations

| Risk | Mitigation |
| --- | --- |
| Jev cannot write prose → UX feels worse | Template narrative is factual and consistent; the structured fields add information prose never had. Keep Gemini only if prose quality is a hard requirement (Option D). |
| Third-party data egress for a "self-hosted" product | Opt-in (`JEV_MODE=off` default), no raw IPs, normalized paths, documented in README security notes, configurable base URL for a proxy. |
| Unknown accuracy on this domain | `observe` mode + `make jev-eval` before `enforce`; confidence buckets make mistakes visible. |
| Model alias drift (`jev-latest`) | Pin `jev-1.14.0`; log `model` per anomaly; thresholds tuned per version. |
| No official Go SDK | Thin client + contract tests against the documented schema; option B (TS sidecar) remains open behind the port. |
| Blocking the 5 s worker | 1.5 s timeout, semaphore 4, breaker; revisit queue only if measured latency demands it. |
| Over-trusting the model | Jev never changes severity or takes action; it only annotates. Deterministic detection stays authoritative. |
| Cost surprise from a noisy project | Per-type dedupe (already) + `JEV_MAX_CALLS_PER_HOUR` + token logging. |

---

## 17. Open questions for you

1. **Runtime:** Go client (recommended) or TS sidecar with the official SDK?
2. **Gemini:** remove entirely, or keep as a prose writer behind the same port?
3. **Data egress:** is sending derived metrics (no raw IPs, normalized paths, project
   name) to `api.typesafe.ai` acceptable for this deployment? If not, do we want a
   proxy/self-hosted gateway or `JEV_MODE=off` by default?
4. **UI scope:** compact diagnosis card (recommended) or a fuller incident panel
   (timeline + probabilities)?
5. **Actions:** stay advisory, or do you eventually want Jev-gated webhook/alert
   routing (Slack/PagerDuty) — which changes the plan's scope materially?
```
