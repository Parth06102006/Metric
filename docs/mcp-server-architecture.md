# Metric MCP Server (Go) + Jev — Final Architecture

Supersedes §1.2/§3 of the first MCP draft. Companion to
`docs/jev-ai-architecture.md` (Jev port, diagnosis model, context pack, schema).

Your decisions are baked in:

| Decision | Implementation |
| --- | --- |
| HTTP transport | MCP Streamable HTTP (`/mcp`), sessions, bearer auth, loopback/internal bind |
| Per-project scoping | Tokens carry an explicit project allowlist; every tool call is checked; admin SSO bypasses |
| Concurrent requests | Per-project limiter + global Jev semaphore + `singleflight` collapse; no global lock |
| Persistence | `analysis_runs` table: every execution stored, replayable, auditable, idempotent |
| `analyze_current_state` | Yes, same catalog, `trigger.rule = "manual_review"` |
| Docs | This file → `docs/mcp-server-architecture.md` |

> Plan-mode note: this file lives in `/home/parth_garg/.opencode/plan/` because that
> is the only writable location in plan mode. Switch to build mode and I will copy it
> to `docs/mcp-server-architecture.md`, or copy it yourself.

---

## 0. TL;DR

```text
Agent (opencode / Claude Code / Codex)
   │  HTTP  POST /mcp     Authorization: Bearer tok_…   Mcp-Session-Id
   ▼
metric-mcp  (Go, Streamable HTTP, stateless tools)
   │  same bearer token, forwarded — MCP holds NO Jev key, NO DB
   ▼
metric-api
   ├─ reads: snapshot, history, endpoints, anomalies, analysis runs
   └─ POST /analyses ──► context pack ──► Jev (8 questions) ──► analysis_runs
                                              └─ deterministic narrative
```

Key correction to the earlier draft: **Jev runs inside the API, not inside the MCP
server.** MCP is a protocol adapter that forwards the agent's token. This gives you:

- one Jev client, one breaker, one budget, one concurrency policy — shared with the
  background anomaly worker;
- persistence **atomic** with the model call (no "computed but not stored" window);
- the TypeSafe key exists in exactly one container;
- MCP becomes a thin, stateless, easily testable layer.

Ten tools, five new API routes, four new tables/changes, two binaries, one module.

---

## 1. Topology

```text
                         ┌───────────────────────────────────────────────┐
  agent A ──┐            │ metric-mcp                                    │
  agent B ──┼── HTTP ───►│  /mcp  Streamable HTTP + sessions             │
  agent C ──┘            │  bearer → principal (agent_id, scopes,        │
                         │            allowed project ids)               │
                         │  per-project concurrency gate                 │
                         │  tool schemas + rate limits + audit           │
                         └───────────────┬───────────────────────────────┘
                                         │  same bearer, forwarded
                                         ▼
                         ┌───────────────────────────────────────────────┐
                         │ metric-api                                    │
                         │  identity → project allowlist → handlers      │
                         │  contextpack.Build()                          │
                         │  jev.Client (singleflight, semaphore, breaker)│
                         │  analysis_runs (SQLite, 1 writer)             │
                         └───────────────┬───────────────────────────────┘
                                         │  POST /v1/systemone
                                         ▼
                                  api.typesafe.ai
```

Concurrency, draw it once:

```text
in-flight agent requests
  ├─ project A ──► sem(2) ─┐
  ├─ project A ──► sem(2) ─┤            ┌─ singleflight(project, anomaly, state_hash)
  ├─ project B ──► sem(2) ─┼──► global ─┤        └─ one Jev call for N duplicates
  └─ project C ──► sem(2) ─┘    sem(4)  └─ breaker(5 fails / 60 s)
```

Different projects run in parallel. The same project is capped, not serialized. The
same question about the same window is collapsed into one model call and one row.

---

## 2. Why Jev moved from MCP to the API

| Criterion | Jev in MCP (draft 1) | **Jev in API (final)** |
| --- | --- | --- |
| Jev key locations | MCP + API (2) | API only (1) |
| Worker and agent share calibration | No (two call sites drift) | Yes (one catalog, one gate) |
| Persistence | MCP computes → second call to store → inconsistency possible | Atomic: compute + store in one handler |
| Budget / breaker | Duplicated | One, shared with the worker |
| MCP complexity | HTTP client + Jev client + breaker + pack | HTTP client + tool schemas only |
| Extra latency | 0 | ~1 ms loopback hop (Jev is ~100 ms) |
| Testability | Needs a Jev fake inside MCP | Fake the analysis endpoint |

Cost of the change: `POST /api/projects/{id}/analyses` must exist and the agent token
needs a narrow `analysis:write` scope. Both are small and are described below.

---

## 3. Auth, tokens, per-project scoping

### 3.1 One token, three layers

The agent token is the **only** credential. It authenticates the MCP transport, is
forwarded to the API, and defines the project allowlist. No shared secrets between MCP
and API beyond this, no Jev key in MCP.

Format: `tok_` + 32 random bytes hex. Stored as SHA-256 only, same pattern as ingest
keys. Constant-time compare.

### 3.2 Schema

```sql
agent_tokens (
  id          TEXT PRIMARY KEY,          -- 8 random bytes hex
  name        TEXT NOT NULL,             -- "claude-desktop", "oncall-agent"
  token_hash  TEXT NOT NULL UNIQUE,      -- sha256 hex
  scopes      TEXT NOT NULL,             -- "read,analysis:write"
  created_at  INTEGER NOT NULL,
  revoked_at  INTEGER                    -- NULL = active
)

agent_token_projects (
  token_id   TEXT NOT NULL REFERENCES agent_tokens(id) ON DELETE CASCADE,
  project_id TEXT NOT NULL,
  PRIMARY KEY (token_id, project_id)
)
-- index: (project_id) for "who can see this project"
```

Minting (no UI, no shell needed in the runtime image — a flag, like `--healthcheck`):

```bash
metric-api --create-agent-token \
  --name claude-desktop \
  --projects a1b2c3d4e5f60718,9f8e7d6c5b4a3210 \
  --scopes read,analysis:write
# prints the token exactly once
```

Revocation: `--revoke-agent-token <id>` (sets `revoked_at`). Works even while sessions
are open: the API rejects at the next call, MCP drops the session on 401.

### 3.3 Identity + middleware

```go
// domain/identity.go (extended)
type Identity struct {
    Email    string
    User     string
    Admin    bool
    AgentID  string                 // non-empty for agent tokens
    Scopes   map[string]struct{}    // read, analysis:write
    Projects map[string]struct{}    // empty + AgentID == "" => SSO user, unscoped
}
```

Order inside the `/api` group (before `Timeout`):

```text
RequestID → StripSlashes → RealIP → Logger → Recoverer
  → Identity (SSO header | dev | agent bearer)
  → RequireProjectAccess   (agents only: projectID ∈ allowlist)
  → Timeout
  → route
```

- `RequireProjectAccess` reads `{projectID}` from the chi URL and returns **404** (not
  403) for a project outside the allowlist, so agents cannot enumerate IDs.
- Admin SSO identities skip the allowlist entirely; `Admin` is never true for agents.
- `RequireAdmin` stays exactly as it is. Agents can never mutate config.
- `RequireScope("analysis:write")` guards the single write route.

### 3.4 `/api/auth/me` (extended)

```json
{
  "email": "mcp-agent@local",
  "admin": false,
  "agent_id": "1a2b3c4d5e6f7081",
  "scopes": ["read", "analysis:write"],
  "projects": ["a1b2c3d4e5f60718"]
}
```

MCP calls this once per session (60 s cache) to learn the scope, then enforces it
locally before making any request — a fast reject instead of a wasted round trip.

---

## 4. HTTP transport

### 4.1 Server wiring

```go
// apps/api/mcp/main.go
srv := mcp.NewServer(
    &mcp.Implementation{Name: "metric", Version: version},
    &mcp.ServerOptions{
        Logger: logger,
        Instructions: instructions,
    },
)
registerTools(srv, deps) // §7

handler := mcp.NewStreamableHTTPHandler(
    func(*http.Request) *mcp.Server { return srv },
    &mcp.StreamableHTTPOptions{
        Stateless:           false,              // sessions carry the principal
        SessionTimeout:      30 * time.Minute,
        MaxRequestBodyBytes: 1 << 20,
        Logger:              logger,
    },
)

mux := http.NewServeMux()
mux.Handle("/mcp", auth.RequireBearerToken(verifier, &auth.RequireBearerTokenOptions{
    Scopes: []string{"read"},
})(handler))

httpServer := &http.Server{
    Addr:              cfg.MCPAddr,   // 127.0.0.1:8090
    Handler:           mux,
    ReadHeaderTimeout: 5 * time.Second,
    IdleTimeout:       120 * time.Second,
    // no WriteTimeout: SSE streams are long-lived (same reasoning as /ws in docs/architecture.md)
}
```

- `Stateless: false` is required for per-session identity and stream resumption.
- The SDK's localhost DNS-rebinding protection stays **on** (default). Do not disable.
- No CORS: MCP clients are not browsers. If a browser client ever appears, add
  `http.NewCrossOriginProtection().Handler(...)` around the mux.
- Bind loopback by default. In compose, bind `0.0.0.0:8090` on the **internal**
  network only, never published.

### 4.2 Token verifier (no second secret)

```go
func (a *Authenticator) Verify(ctx context.Context, token string, _ *http.Request) (*auth.TokenInfo, error) {
    p, err := a.lookup(ctx, token) // GET {METRIC_API_URL}/api/auth/me, 2 s timeout, 60 s cache
    if err != nil {
        return nil, auth.ErrInvalidToken
    }
    return &auth.TokenInfo{
        UserID:     p.AgentID,              // SDK uses this to bind sessions to one user
        Scopes:     p.Scopes,
        Expiration: time.Now().Add(a.cacheTTL),
        Extra:      map[string]any{"projects": p.Projects},
    }, nil
}
```

- `UserID` blocks session hijacking: a stolen `Mcp-Session-Id` from another token is
  rejected by the transport.
- `Expiration` equals the cache TTL, so a revoked token stops working within 60 s even
  if cached.
- Every downstream API call forwards `Authorization: Bearer <same token>`. No service
  credential is stored in MCP config.

### 4.3 Tool-side scoping

```go
func principal(ctx context.Context) (Principal, error) {
    ti := auth.TokenInfoFromContext(ctx)
    if ti == nil { return Principal{}, errNoAuth }
    return Principal{AgentID: ti.UserID, Scopes: ti.Scopes, Projects: ti.Extra["projects"]}, nil
}

func (p Principal) Can(projectID string) bool { /* allowlist + admin bypass */ }
```

Each project-scoped handler starts with `if !p.Can(in.ProjectID) { return 404-style }`.

### 4.4 Client config

opencode:

```jsonc
{
  "mcp": {
    "metric": {
      "type": "remote",
      "url": "http://127.0.0.1:8090/mcp",
      "headers": { "Authorization": "Bearer tok_…" },
      "enabled": true
    }
  }
}
```

Claude Code:

```bash
claude mcp add --transport http metric http://127.0.0.1:8090/mcp \
  --header "Authorization: Bearer tok_…"
```

Both support `Mcp-Session-Id` transparently; no extra configuration.

---

## 5. Concurrency model

| Layer | Mechanism | Knob | Default |
| --- | --- | --- | --- |
| HTTP transport | Go's `net/http` — one goroutine per request | — | — |
| MCP per-project gate | `map[projectID]chan struct{}`, lazily created | `MCP_PER_PROJECT_CONCURRENCY` | 2 |
| MCP global | `errgroup.SetLimit` around the handler | `MCP_MAX_INFLIGHT` | 8 |
| MCP rate | token bucket per token | `MCP_MAX_CALLS_PER_MIN` | 60 |
| API per-project (Jev) | semaphore **shared with the worker** | `JEV_PER_PROJECT_CONCURRENCY` | 2 |
| API global (Jev) | semaphore | `JEV_MAX_INFLIGHT` | 4 |
| Duplicate collapse | `singleflight.Group` keyed `project|anomaly|state_hash` | — | — |
| Budget | token bucket per hour | `JEV_MAX_CALLS_PER_HOUR` | 120 |
| Failure | consecutive-failure breaker | `JEV_BREAKER_FAILS/TTL` | 5 / 60 s |

Rules:

1. **Never serialize globally.** A single mutex or a `SetMaxOpenConns(1)`-style queue for
   tool calls would make three independent projects queue behind each other. The only
   shared state is the SQLite writer, and that is on the API side, is a single short
   insert, and is already serialized by design.
2. **Cap, don't block.** Per-project gates use `select { case sem <- struct{}{}: case
   <-ctx.Done(): }` with the request context, so a slow project returns a clean
   "busy, retry" instead of holding an agent's connection until the transport timeout.
3. **Collapse duplicates.** Five agents asking about incident #42 within the reuse
   window produce one Jev call and five responses. `request_key` (below) makes this
   work across processes too if you ever run two API instances.
4. **Timeouts are nested and decreasing:** MCP request `MCP_CALL_TIMEOUT=8s` → API
   middleware `API_TIMEOUT=15s` (unchanged) → Jev `JEV_TIMEOUT=1500ms` → API HTTP client
   to Jev 2 s. No layer can outlive its parent.
5. **Cancellation propagates.** Agent disconnects → request context cancelled → API call
   → Jev call cancelled; the run is not persisted as `error` if the cause is
   cancellation (store nothing rather than noise).

---

## 6. Persistence ("remember the execution")

### 6.1 What is stored

| Table | Row | Retention |
| --- | --- | --- |
| `analysis_runs` | one per Jev execution (agent or worker), full diagnosis + narrative + tokens + model + state hash | `ANALYSIS_RETENTION` (90 d), pruned hourly |
| `anomalies` | latest diagnosis denormalized (columns) for the dashboard's hot path | `SNAPSHOT_RETENTION` (existing) |
| `audit_events` | one `mcp.tool_call` per tool invocation (tool, project, run_id, outcome, duration) | `AUDIT_RETENTION` (existing) |

Reusing `audit_events` for tool calls avoids a fourth table and puts agent activity in
the same admin feed as key rotations and denials.

### 6.2 Schema

```sql
analysis_runs (
  id             INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id     TEXT NOT NULL,
  anomaly_id     INTEGER,                 -- NULL for analyze_current_state
  trigger_kind   TEXT NOT NULL,           -- anomaly | manual_review
  trigger_rule   TEXT NOT NULL,           -- high_latency | manual_review | ...
  actor          TEXT NOT NULL,           -- mcp-agent:<agent_id> | worker
  request_key    TEXT NOT NULL,           -- sha256(project|anomaly|state_hash|model|catalog)
  state_hash     TEXT NOT NULL,           -- sha256 of the redacted pack
  state_bytes    INTEGER NOT NULL,
  catalog_version TEXT NOT NULL,          -- "v1"
  status         TEXT NOT NULL,           -- verified | unverified | insufficient_evidence | disabled | error
  pattern        TEXT,
  primary_signal TEXT,
  action         TEXT,
  urgency        REAL,
  confidence     REAL,
  needs_human    INTEGER NOT NULL DEFAULT 0,
  evidence_ok    INTEGER NOT NULL DEFAULT 0,
  model          TEXT,
  input_tokens   INTEGER NOT NULL DEFAULT 0,
  output_tokens  INTEGER NOT NULL DEFAULT 0,
  elapsed_ms     INTEGER NOT NULL DEFAULT 0,
  narrative      TEXT NOT NULL,
  diagnosis_json TEXT NOT NULL,
  created_at     INTEGER NOT NULL
)
CREATE UNIQUE INDEX analysis_runs_request_key ON analysis_runs(request_key);
CREATE INDEX analysis_runs_project_time ON analysis_runs(project_id, created_at DESC);
CREATE INDEX analysis_runs_anomaly      ON analysis_runs(project_id, anomaly_id, created_at DESC);
```

Why `state_hash` and not the pack: the pack contains endpoint paths (potentially
sensitive). Store the hash + byte count for dedupe and audit; the pack itself is
rebuilt on demand. `diagnosis_json` keeps the raw answers for evaluation.

### 6.3 Idempotency / reuse

```text
POST /api/projects/{id}/analyses { "anomaly_id": 42, "reuse_seconds": 120 }
  key := sha256(project | anomaly | state_hash | model | catalog_version)
  existing := SELECT ... WHERE request_key = key AND created_at >= now - reuse_seconds
  if existing != nil  -> 200 { run, "cached": true }
  else               -> singleflight(key) -> Jev -> INSERT (UNIQUE) -> 201 { run, "cached": false }
```

`UNIQUE(request_key)` makes the collapse guarantee hold even without singleflight; on
conflict, read the winning row and return it. `state_hash` changes when the metrics
window moves, so a genuinely new state is always analysed afresh.

### 6.4 Read-back ("what did we already do?")

| Route | Returns |
| --- | --- |
| `GET /api/projects/{id}/analyses?anomaly_id=&limit=20` | runs, newest first (narrative included) |
| `GET /api/projects/{id}/analyses/{runID}` | one run, full `diagnosis_json` |

MCP exposes these as `list_analyses` (one tool, `anomaly_id` optional). This is the
"rememberance" the agent needs: before analysing, it can see that a run exists, what it
decided, when, and by whom.

### 6.5 Worker and agent share one path

`anomaly.Usecase` calls the same `analysis.Service.Run(ctx, Request{...})` with
`actor="worker"` and `trigger_kind="anomaly"`. No second code path, no divergence in
calibration or gating. The only difference is who triggers it and whether the reuse
window applies (the worker's per-type dedupe already prevents repeats).

---

## 7. Tools (10)

All project-scoped tools validate `project_id` against the token allowlist. All return
`structuredContent` plus a short `summary` string.

| # | Tool | Input | Writes | Persists |
| --- | --- | --- | --- | --- |
| 1 | `list_projects` | — | no | no |
| 2 | `get_live_metrics` | `project_id` | no | no |
| 3 | `get_metric_history` | `project_id`, `minutes=60` | no | no |
| 4 | `get_endpoints` | `project_id`, `top=5` | no | no |
| 5 | `get_thresholds` | `project_id` | no | no |
| 6 | `list_anomalies` | `project_id`, `limit=20`, `since_minutes?`, `type?` | no | no |
| 7 | `get_incident_context` | `project_id`, `anomaly_id?` | no | no |
| 8 | `analyze_incident` | `project_id`, `anomaly_id`, `reuse_seconds?` | yes (run) | yes |
| 9 | `analyze_current_state` | `project_id`, `reuse_seconds?` | yes (run) | yes |
| 10 | `list_analyses` | `project_id`, `anomaly_id?`, `limit=10` | no | reads runs |

Annotations: 1–7 and 10 are `ReadOnlyHint: true, IdempotentHint: true`. Tools 8–9 are
`ReadOnlyHint: true` (they don't change configuration), `IdempotentHint: false`
(non-deterministic, billable), `OpenWorldHint: true` (external model). The regression
test asserts exactly this, so a mutating tool cannot be added unnoticed.

### 7.1 The two analysis tools

```go
type AnalyzeIncidentInput struct {
    ProjectID    string `json:"project_id" jsonschema:"project id from list_projects"`
    AnomalyID    int64  `json:"anomaly_id" jsonschema:"anomaly id from list_anomalies"`
    ReuseSeconds int    `json:"reuse_seconds,omitempty" jsonschema:"reuse a stored run younger than this; default 120"`
}

type AnalyzeOutput struct {
    RunID      int64             `json:"run_id"`
    Cached     bool              `json:"cached"`
    Status     string            `json:"status"`
    Pattern    string            `json:"pattern"`
    Action     string            `json:"action"`
    Urgency    float64           `json:"urgency"`
    Confidence float64           `json:"confidence"`
    NeedsHuman bool              `json:"needs_human"`
    Model      string            `json:"model"`
    Narrative  string            `json:"narrative"`
    Evidence   map[string]any    `json:"evidence"`   // baseline ratios, endpoint concentration
    Notice     string            `json:"notice"`     // advisory + untrusted-data reminder
}
```

`analyze_current_state` is the same handler with `anomaly_id = 0`; the API builds a pack
with `trigger = {"rule":"manual_review","severity":"info"}`, and the shared catalog
answers `healthy` / `degrading` / … . One catalog, two entry points (`catalog_version`
"v1").

### 7.2 Failure behaviour

| Situation | Result |
| --- | --- |
| Jev disabled / no key | `status:"disabled"` + rule-based narrative, **not** a tool error |
| Jev 429 after retries / breaker open | `status:"error"` + rule-based narrative, run stored |
| Project outside allowlist | tool error, message "project not found" (no existence leak) |
| Missing `analysis:write` scope | tool error: "this token can read but not run analyses" |
| Agent disconnects mid-call | nothing stored (cancellation, not failure) |
| Concurrent identical requests | one Jev call; all callers get the same `run_id`, `cached:true` for the rest |

### 7.3 Descriptions are the API

The agent chooses tools from descriptions alone. Use imperative text with
preconditions and anti-uses; e.g.:

> `analyze_incident` — Run the calibrated diagnosis for one anomaly. Call after
> `get_incident_context` if you need the raw evidence, or directly for just the decision.
> Persists a run; repeat calls within the reuse window return it for free. Advisory only
> — never change thresholds or delete projects based on this output.

---

## 8. Resources and prompt

| Kind | Name | Content |
| --- | --- | --- |
| Resource | `metric://projects` | the token's project list |
| Resource template | `metric://projects/{projectID}/snapshot` | latest snapshot |
| Resource template | `metric://projects/{projectID}/context` | current Jev pack |
| Prompt | `triage_incident` | `(project_id, anomaly_id?)` → list anomalies → context → analyze → report status/confidence/evidence; never suggest a mutation |

Prompts are how you encode the workflow so the agent doesn't rediscover it each session.

---

## 9. API surface added

| Method | Path | Auth | New |
| --- | --- | --- | --- |
| `GET` | `/api/auth/me` | any identity | extended fields |
| `GET` | `/api/projects/{id}/metrics/history?minutes=60` | identity + allowlist | yes |
| `GET` | `/api/projects/{id}/endpoints?top=10` | identity + allowlist | yes |
| `GET` | `/api/projects/{id}/context?anomaly_id=` | identity + allowlist | yes |
| `POST` | `/api/projects/{id}/analyses` | `analysis:write` | yes |
| `GET` | `/api/projects/{id}/analyses` | identity + allowlist | yes |
| `GET` | `/api/projects/{id}/analyses/{runID}` | identity + allowlist | yes |
| `POST` | `/api/projects/{id}/agent-tokens` … | **not exposed** — CLI only | — |

Everything else stays as documented in README/architecture.

---

## 10. Files affected

### New — MCP binary

| Path | Purpose |
| --- | --- |
| `apps/api/mcp/main.go` | flags, config, HTTP server, graceful shutdown, `--healthcheck` |
| `apps/api/internal/mcp/server.go` | `New()`, instructions, tool registration, middleware (auth, limits, audit) |
| `apps/api/internal/mcp/principal.go` | principal extraction, allowlist check, scope check |
| `apps/api/internal/mcp/tools_read.go` | tools 1–6 |
| `apps/api/internal/mcp/tools_context.go` | tool 7 |
| `apps/api/internal/mcp/tools_analysis.go` | tools 8–9 |
| `apps/api/internal/mcp/tools_runs.go` | tool 10 |
| `apps/api/internal/mcp/resources.go`, `prompts.go` | resources + `triage_incident` |
| `apps/api/internal/mcp/errors.go` | domain error → safe tool error |
| `apps/api/internal/mcp/*_test.go` | per-tool + contract + annotations |
| `apps/api/internal/metricapi/client.go` | typed API client (bearer forward, timeouts, retries, no Jev) |

### New — analysis service (API side)

| Path | Purpose |
| --- | --- |
| `apps/api/domain/analysis.go` | `AnalysisRun`, `AnalysisRequest`, status/pattern consts |
| `apps/api/domain/incident.go` | `IncidentContext`, `Baseline`, `EndpointSummary`, labels |
| `apps/api/domain/diagnosis.go` | `Diagnosis`, gating, `Interpret` |
| `apps/api/domain/narrative.go` | deterministic prose |
| `apps/api/domain/summary.go` | `Summarize(records, top)` |
| `apps/api/analysis/service.go` | **single** Run() path: pack → jev → gate → narrative → persist |
| `apps/api/analysis/repository/sqlite/` | `analysis_runs` adapter |
| `apps/api/internal/jev/{client,questions,adapter}.go` | TypeSafe HTTP, catalog, mapping |
| `apps/api/internal/contextpack/{pack,redact,budget}.go` | pack builder + redaction + truncation |
| `apps/api/metrics/baseline.go` | `ComputeBaseline` |
| `apps/api/agent/token.go` | mint/verify/revoke tokens, CLI helpers |

### Modified

| Path | Change |
| --- | --- |
| `apps/api/go.mod` | `+ github.com/modelcontextprotocol/go-sdk v1.8.0`, `golang.org/x/sync` |
| `apps/api/internal/sqlite/db.go` | schema v3: anomaly columns, `analysis_runs`, `agent_tokens`, `agent_token_projects` |
| `apps/api/internal/sqlite/db_test.go` | v2 → v3 migration test |
| `apps/api/internal/middleware/identity.go` | bearer agent branch |
| `apps/api/internal/middleware/project_access.go` | **new**, allowlist + `RequireScope` |
| `apps/api/internal/middleware/identity_test.go` | allowlist 404, scope 403, revocation, admin bypass |
| `apps/api/internal/rest/identity_handler.go` | extended `/auth/me` |
| `apps/api/internal/rest/metrics_handler.go` | `History`, `Endpoints` |
| `apps/api/internal/rest/analysis_handler.go` | **new**: context, run, list, get |
| `apps/api/internal/server/server.go` | new routes + middleware order |
| `apps/api/internal/config/config.go` | `AGENT_*`, `MCP_*`, `JEV_*` (replace `Gemini*`) |
| `apps/api/app/main.go` | wire `analysis.Service`, worker, CLI flags |
| `apps/api/anomaly/usecase.go` | call `analysis.Service.Run(actor="worker")` |
| `apps/api/anomaly/repository/sqlite/repository.go` | latest-diagnosis columns |
| `apps/api/internal/llm/` | **delete** |
| `apps/api/Dockerfile` | `ARG BINARY=api` → build `metric-api` or `metric-mcp` |
| `compose.yaml` | optional `mcp` service, internal network, no published port |
| `Makefile` | `make mcp`, `make token`, `make mcp-smoke` |
| `.env.example`, `README.md`, `docs/architecture.md` | config, component, auth, flows |
| `apps/web/src/types/api.ts`, `ProjectDetail.tsx` | optional diagnosis card (companion doc §12) |

---

## 11. Config

| Variable | Default | Where | Purpose |
| --- | --- | --- | --- |
| `METRIC_API_URL` | `http://localhost:8080` | MCP | API base URL |
| `MCP_ADDR` | `127.0.0.1:8090` | MCP | bind address |
| `MCP_PUBLIC_URL` | empty | MCP | used in `WWW-Authenticate` metadata, optional |
| `MCP_SESSION_TIMEOUT` | `30m` | MCP | idle session reaping |
| `MCP_MAX_INFLIGHT` | `8` | MCP | global concurrent tool calls |
| `MCP_PER_PROJECT_CONCURRENCY` | `2` | MCP | per-project gate |
| `MCP_MAX_CALLS_PER_MIN` | `60` | MCP | per-token rate limit |
| `MCP_CALL_TIMEOUT` | `8s` | MCP | end-to-end tool deadline |
| `AGENT_TOKEN_CACHE_TTL` | `60s` | MCP | `/auth/me` cache; also the revocation lag |
| `TYPESAFE_API_KEY` | empty | **API only** | Jev key; empty ⇒ `status:"disabled"` |
| `JEV_MODEL` | `jev-1.14.0` | API | pinned versioned model |
| `JEV_BASE_URL` | `https://api.typesafe.ai` | API | override for a proxy |
| `JEV_TIMEOUT` | `1500ms` | API | per-call deadline |
| `JEV_MIN_CONFIDENCE` | `0.55` | API | below ⇒ `unverified` |
| `JEV_MAX_INFLIGHT` | `4` | API | global Jev gate |
| `JEV_PER_PROJECT_CONCURRENCY` | `2` | API | per-project Jev gate |
| `JEV_MAX_CALLS_PER_HOUR` | `120` | API | budget |
| `JEV_REUSE_SECONDS` | `120` | API | idempotency window |
| `ANALYSIS_RETENTION` | `2160h` | API | run retention |
| `JEV_MODE` | `observe` | API | `off` / `observe` / `enforce` |

Production guards: `JEV_MODE != off && TYPESAFE_API_KEY == ""` → startup error;
`MCP_ADDR` must be loopback unless `MCP_ALLOW_PUBLIC=true`; MCP refuses to start without
`METRIC_API_URL`.

---

## 12. Testing

Offline (`make check`):

1. `internal/mcp` — in-memory transport (`mcp.NewInMemoryTransports()`), one test per
   tool: success, allowlist denial, missing scope, API 500, Jev-disabled passthrough.
2. `internal/mcp/contract_test.go` — golden `tools/list` JSON (names, descriptions,
   schemas, annotations). Fails on accidental renames that break saved agent configs.
3. `internal/middleware` — token accepted, wrong token 401, revoked 401, allowlist 404,
   `analysis:write` 403, admin bypass.
4. `analysis/service` — reuse window returns `cached`, `UNIQUE` conflict path, breaker
   open → `status:"error"` + narrative, cancellation stores nothing.
5. `contextpack` — golden pack, no IPs/UUIDs, budget truncation order.
6. `internal/jev` — `httptest` fake: 200, 401, 422, 429 retry, 529, timeout, unknown
   label, malformed body.
7. `domain` — gating boundaries, narrative goldens.
8. SQLite — migration v2 → v3, indexes, retention prune.

Live:

9. `make mcp-smoke` — curl the HTTP transport end to end (initialize → tools/list →
   tools/call) against a running API, asserting shapes.
10. `make jev-eval` — ~30 synthetic incidents, per-label accuracy, mean confidence,
    calibration buckets. **Do not set `JEV_MODE=enforce` before this is reviewed.**

---

## 13. What you need (prerequisites)

| Need | Detail |
| --- | --- |
| Go 1.27.x | **`go.mod` says `go 1.27.2`; this machine has `go1.26.8`.** Run `go env -w GOTOOLCHAIN=auto` (it will fetch 1.27.2) or install 1.27.x. `go build ./...` fails otherwise. |
| Module deps | `github.com/modelcontextprotocol/go-sdk@v1.8.0`, `golang.org/x/sync` |
| TypeSafe account | API key from `console.typesafe.ai/keys`; check `/v1/models` for the current `jev-*` ID |
| Bun 1.3+ | only if you touch the SPA (already installed in this repo) |
| An MCP client | opencode (remote HTTP), Claude Code (HTTP transport), or `curl` for the smoke test |
| A local API | `make api-dev` with `AUTH_MODE=dev`, plus at least one project with ingested traffic |
| Nothing else | no Docker change is required to develop; compose is for deploy |

---

## 14. Docs to read (ordered, with what to extract)

### TypeSafe / Jev — do this first

1. `https://docs.typesafe.ai/introduction.md` — what a System One model is. Extract: it
   decides, it does not generate prose.
2. `https://docs.typesafe.ai/concepts/system-one.md` — primitives table. Extract: which
   primitive answers your question type.
3. `https://docs.typesafe.ai/concepts/state.md` — state shapes. Extract: object with
   named fields, one state per request.
4. `https://docs.typesafe.ai/primitives.md` + `/primitives/noul.md`, `/choice.md`,
   `/score.md` — exact fields, `criteria` format, what each answer contains.
5. `https://docs.typesafe.ai/confidence.md` — `confidence` shape, `|2p−1|` for noul,
   0.7/0.3 starting thresholds. **This is where the gating in §6 of the companion doc
   comes from.**
6. `https://docs.typesafe.ai/concepts/how-to-build-with-system-one.md` — decompose,
   structure, fan out, compose in code, route on uncertainty. This is the design method
   for §7's catalog.
7. `https://docs.typesafe.ai/patterns/fan-out.md` and `/confidence-routing.md` — one
   request, many questions; act vs escalate.
8. `https://docs.typesafe.ai/api/system-one.md` — the exact request/response JSON and
   the OpenAPI schema. Use it to write `internal/jev/client.go`.
9. `https://docs.typesafe.ai/api/errors.md` — 429/529/413/415, compression. Use it for
   retry and body limits.
10. `https://docs.typesafe.ai/models.md` — model IDs, aliases, context limits, price.
    Pin `jev-1.14.0`; log the returned `model`.

### MCP — then this

11. `https://modelcontextprotocol.io/specification/2026-07-28` — transports (Streamable
    HTTP, `Mcp-Session-Id`, SSE), tools, resources, prompts.
12. `https://modelcontextprotocol.io/specification/2026-07-28/basic/security_best_practices`
    — token binding, session hijacking, localhost/DNS rebinding. Explains §4.2.
13. Go SDK `docs/quick_start.md` and `internal/docs/server.src.md` — `AddTool`, typed
    input/output, `jsonschema` tags, resources, prompts.
14. Go SDK `examples/auth-middleware` and `examples/streamable` — bearer middleware and
    HTTP wiring you can copy.
15. Go SDK `design/design.md` — `ToolHandlerFor`, `CallToolResult`, structured content.
16. `pkg.go.dev/github.com/modelcontextprotocol/go-sdk/mcp` — the exact field names you
    will type.

### Go — as needed

17. `net/http` `Server` timeouts and `http.CrossOriginProtection` — §4.1.
18. `golang.org/x/sync/singleflight` and `/semaphore` — §5.
19. `log/slog` structured logging (already used everywhere in this repo).

### Repo — read before writing code

20. `docs/architecture.md` §3 (flows), §4 (structure), §5 (data model), §10 (tradeoffs).
21. `docs/jev-ai-architecture.md` — ports, `Diagnosis`, pack, migration.
22. `apps/api/internal/middleware/identity.go` — where the token branch goes.
23. `apps/api/internal/sqlite/db.go` — the `user_version` migration pattern to copy.
24. `apps/api/internal/ws/hub.go` — the repo's existing concurrency idiom (mutex +
    non-blocking send); keep the same style.
25. `apps/api/internal/server/server.go` — middleware order you are extending.

---

## 15. Implementation runbook

Each step ends with a gate. Do not start the next step until the gate passes.

### Step 0 — toolchain

```bash
go env -w GOTOOLCHAIN=auto
cd apps/api && go build ./... && go test ./...
```

Gate: builds and tests pass with 1.27.2 resolved.

### Step 1 — schema v3

1. Add to `internal/sqlite/db.go`: anomaly diagnosis columns, `analysis_runs`,
   `agent_tokens`, `agent_token_projects`, indexes, `schemaVersion = 3`.
2. Mirror in `apps/api/domain/analysis.go` structs.
3. Migration test: build a v2 DB, run `Migrate`, assert columns/tables and that old rows
   read with `analysis_status = "rule_based"`.

```bash
go test ./internal/sqlite -run Migrate -v
```

Gate: migration is idempotent (run it twice), old data intact.

### Step 2 — agent tokens + scoping

1. `apps/api/agent/token.go`: mint (32 random bytes, SHA-256, `tok_` prefix), verify
   (constant-time), revoke.
2. `main.go`: `--create-agent-token`, `--revoke-agent-token`, `--list-agent-tokens`.
3. `identity.go`: bearer branch → `domain.Identity{AgentID, Scopes, Projects, Admin:false}`.
4. `middleware/project_access.go`: allowlist (404) + `RequireScope` (403).
5. Extend `/api/auth/me`.
6. Tests: accepted, wrong, revoked, out-of-scope, admin bypass.

```bash
make api-dev
TOK=$(./bin/metric-api --create-agent-token --name dev --projects <PID> --scopes read,analysis:write)
curl -s localhost:8080/api/auth/me -H "Authorization: Bearer $TOK" | jq
curl -s -o /dev/null -w '%{http_code}\n' localhost:8080/api/projects/<OTHER> -H "Authorization: Bearer $TOK"   # 404
curl -s -o /dev/null -w '%{http_code}\n' -X PUT localhost:8080/api/projects/<PID>/thresholds -H "Authorization: Bearer $TOK" -d '{}'  # 403
```

Gate: 200 / 404 / 403 exactly as above, and an `mcp-agent@local` row appears in
`/api/audit`.

### Step 3 — data endpoints

`GET /metrics/history`, `GET /endpoints`, `GET /context` on the handler + usecase
(`metrics.Usecase.History/Endpoints`, `contextpack.Build`). Gate:

```bash
curl -s "localhost:8080/api/projects/$PID/metrics/history?minutes=60" -H "Authorization: Bearer $TOK" | jq '.[0]'
curl -s "localhost:8080/api/projects/$PID/endpoints?top=3" -H "Authorization: Bearer $TOK" | jq
curl -s "localhost:8080/api/projects/$PID/context" -H "Authorization: Bearer $TOK" | jq '.state_baseline // .state'
```

Gate: no IPv4/IPv6 literal and no UUID anywhere in `/context` output.

### Step 4 — Jev client + analysis service

1. `internal/jev`: client, catalog (`v1`), adapter; tests with `httptest`.
2. `analysis.Service.Run`: pack → gating → narrative → persist → return.
3. `POST /analyses` + `GET /analyses[/{runID}]`.
4. Wire the worker to the same service (`actor="worker"`).

```bash
curl -s -X POST "localhost:8080/api/projects/$PID/analyses" \
  -H "Authorization: Bearer $TOK" -H 'Content-Type: application/json' \
  -d '{"anomaly_id": 42}' | jq '{run_id,status,pattern,confidence,cached}'
# repeat immediately: cached:true, same run_id
```

Gate: second call returns `cached:true` and does not hit TypeSafe (check the log line).
Accuracy gate for `enforce`: `make jev-eval` reviewed.

### Step 5 — MCP HTTP server

1. `internal/metricapi` typed client (forwards bearer).
2. `internal/mcp`: server, principal, ten tools, resources, prompt.
3. `apps/api/mcp/main.go`: flags, `/mcp`, auth middleware, limits, shutdown.
4. `--healthcheck` calls `/api/auth/me` with a configured token (or just `/health`).

```bash
MCP_ADDR=127.0.0.1:8090 METRIC_API_URL=http://localhost:8080 ./bin/metric-mcp &

# initialize
curl -s -D /tmp/h.txt localhost:8090/mcp \
  -H "Authorization: Bearer $TOK" -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2026-07-28","capabilities":{},"clientInfo":{"name":"curl","version":"0"}}}'

SID=$(grep -i '^mcp-session-id:' /tmp/h.txt | awk '{print $2}' | tr -d '\r')

curl -s localhost:8090/mcp -H "Authorization: Bearer $TOK" -H "Mcp-Session-Id: $SID" \
  -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","method":"notifications/initialized"}'

curl -s localhost:8090/mcp -H "Authorization: Bearer $TOK" -H "Mcp-Session-Id: $SID" \
  -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":2,"method":"tools/list"}' | jq '.result.tools[].name'

curl -s localhost:8090/mcp -H "Authorization: Bearer $TOK" -H "Mcp-Session-Id: $SID" \
  -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"analyze_current_state","arguments":{"project_id":"'$PID'"}}}' \
  | jq '.result.structuredContent | {status,pattern,confidence,narrative}'
```

Gate: `tools/list` returns exactly 10 names; the analysis call returns a `run_id` and a
narrative; an out-of-scope project returns an error; no token → 401.

### Step 6 — client + end to end

Add the opencode/Claude config from §4.4, start a real session, ask "why did checkout
slow down?" and confirm the agent calls `list_anomalies` → `analyze_incident` and cites
the baseline ratio and confidence.

Gate: agent output matches the stored run (`run_id`) and never claims to have changed
anything.

### Step 7 — hardening + docs

Breaker tuning, budget, audit entries for tool calls, retention pruner, load test with
`hey`/`vegeta` against `/mcp` with 3 concurrent projects, update `docs/architecture.md`,
README config table, `.env.example`, compose `mcp` service.

Gate: 50 concurrent calls across 3 projects complete with per-project latency isolation;
breaker opens and serves fallbacks under a Jev outage drill.

---

## 16. Phases

| Phase | Deliverable | Rollback |
| --- | --- | --- |
| 1 | schema v3 + tokens + scoping | migration is additive; drop columns/tables |
| 2 | read endpoints | routes unused |
| 3 | Jev + analysis service + POST `/analyses`, `JEV_MODE=observe` | `JEV_MODE=off` |
| 4 | MCP HTTP + 8 read/analysis tools | stop the mcp process |
| 5 | `analyze_current_state`, `list_analyses`, resources, prompt | unused tools |
| 6 | concurrency + breaker + retention + eval | config only |

---

## 17. Risks

| Risk | Mitigation |
| --- | --- |
| Jev label accuracy on this domain unknown | `observe` mode + `make jev-eval` with confidence buckets before `enforce` |
| Agent acts on an advisory diagnosis | Server instructions + `needs_human` + no mutating tools + API denies writes |
| Prompt injection via endpoint paths | Pack marks data as data, paths normalized, closed-set questions |
| SQLite write contention under many agents | Runs are short inserts; one writer already; reuse window removes most writes |
| Token sprawl | One table, CLI mint/revoke, 60 s revocation lag, audit attribution |
| Model alias drift | pin `jev-1.14.0`, store `model` per run, thresholds per version |
| MCP exposed publicly by mistake | loopback default, `MCP_ALLOW_PUBLIC` explicit, bearer required, no CORS |
| `state_hash` misses a material change | pack version + catalog version are part of the key; bumping either invalidates reuse |

---

## 18. Deltas to `docs/jev-ai-architecture.md`

| Companion section | Change |
| --- | --- |
| §3 where Jev runs | **API, not MCP**; MCP is a protocol adapter only |
| §5 ports | `Analyst` unchanged; add `analysis.Service.Run` as the single entry point |
| §7 context pack | produced/consumed only inside the API |
| §9 files | add `analysis/`, `agent/`, `internal/mcp/`, `internal/metricapi/`, `apps/api/mcp/` |
| §10 data model | add `analysis_runs`, `agent_tokens`, `agent_token_projects`; anomaly diagnosis columns stay |
| §12 API/UI | add the routes in §9 here; UI card unchanged |
| §15 MCP | superseded by this document |

---

## 19. Open questions

1. **`analyze_current_state` trigger text** — should the narrative say "manual review"
   or "scheduled health check"? It is one string in `domain/narrative.go`.
2. **Run retention** — 90 days enough, or keep analysis runs forever (one row ≈ 1 KB)?
3. **Agent-visible scope** — is `read` implicit with `analysis:write`, or explicit (as
   specified here)?
4. **Token minting** — CLI only (as specified), or also an admin API route plus a small
   UI panel?
5. **Approval flow** — should a run with `needs_human=true` require a second tool call
   to "accept" the recommendation before an agent may propose a mitigation? This would
   add an 11th tool and a state machine; recommend deferring.
