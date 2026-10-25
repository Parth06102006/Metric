# Architecture

## 1. Context

Metric is a self-hosted observability service for HTTP APIs. It accepts request
logs from instrumented services, computes a rolling live snapshot, persists
history, raises threshold anomalies, and streams both to a browser dashboard.

The repo is a monorepo with two independently built applications:

- `apps/api` — Go API, clean architecture, SQLite.
- `apps/web` — React SPA, built by Vite and served as static files.

At deploy time Caddy reunites them under one origin. There is no CORS layer and
no server-side session in Go: the edge authenticates people and asserts their
identity with headers.

### Goals

- Keep the domain independent of HTTP, SQL, and the LLM provider.
- Serve browser and machine traffic from one origin with two different auth
  mechanisms (SSO for people, API keys for ingest).
- Stream updates without putting every sample through React state.
- Keep deployment to a single VPS and a single database file.

### Non-goals

- Multi-region or horizontal scaling. The in-memory request window and SQLite
  make this a single-writer system.
- Long-term raw request storage. Only computed snapshots and anomalies are
  persisted.
- A general-purpose alerting system. Rules are fixed and threshold-based.

## 2. Topology

```text
                    ┌──────────────────────────────────────────┐
  browser ──HTTPS──▶│ Caddy (edge)                             │
                    │  TLS, security headers, SPA static files │
                    │                                          │
                    │  /api/*  ──forward_auth──┐               │
                    │  /ws/*   ──forward_auth──┤               │
                    │                          ▼               │
                    │                 oauth2-proxy :4180       │
                    │                          │               │
                    │  /ingest/*  /health      │               │
                    └──────────┬───────────────┼───────────────┘
                               │               │ X-Auth-Request-Email
                               ▼               ▼
                    ┌──────────────────────────────────────────┐
  collector ───────▶│ Go API :8080                             │
  (X-API-Key)       │  chi router + middleware                 │
                    │  usecases ──▶ repositories               │
                    │                 │                        │
                    │        in-memory records + SQLite        │
                    └──────────────────────────────────────────┘
```

Only `edge` publishes ports (`80`, `443`). `api` and `oauth2-proxy` sit on an
internal Docker network. Direct access to the API is impossible from outside
the host.

## 3. Request flows

### Control plane — `/api/*`

1. Caddy `forward_auth` calls `oauth2-proxy:4180/oauth2/auth`.
2. oauth2-proxy returns 202 with `X-Auth-Request-Email` and
   `X-Auth-Request-User` on success, or 401.
3. Caddy copies those headers to the upstream request and adds
   `X-Proxy-Secret`.
4. Go's identity middleware validates the optional secret, requires an email,
   and stores a `domain.Identity` on the request context.
5. `RequireAdmin` guards project creation and threshold updates. With an empty
   `ADMIN_EMAILS`, nobody is an admin: the behavior is fail-closed.
6. The handler decodes JSON, calls one usecase, and writes the result. Domain
   errors become 400/401/403/404/409/500 in `internal/httpx`.

### Ingest — `/ingest/{projectID}`

1. Caddy routes directly to the API; SSO is deliberately bypassed.
2. The handler requires `application/json` and caps the body at 1 MiB.
3. The ingest usecase checks `X-API-Key` against the stored SHA-256 hash using
   `subtle.ConstantTimeCompare`. A missing project and a wrong key both return
   401, so project IDs cannot be enumerated.
4. Records are validated (timestamp, endpoint, method, status, latency, payload,
   parseable IP), then appended to the project's in-memory rolling window.
5. Response: `202 {"accepted": n}`.

### Live stream — `/ws/{projectID}`

1. Caddy authenticates the handshake exactly like `/api`, then proxies with
   `flush_interval -1`.
2. The Go handler checks the project exists, upgrades the connection, and
   registers a subscriber with the hub.
3. It immediately writes a snapshot computed from the current window, then
   forwards hub messages until the client disconnects or its read deadline
   expires.
4. The hub sends a ping every 30 seconds; the read deadline is 60 seconds.

Origin policy: same-host handshakes are allowed (Caddy preserves `Host`), as are
hosts listed in `ALLOWED_ORIGINS`. A missing `Origin` is allowed for non-browser
clients.

### Background pipeline

The anomaly worker ticks every `WORKER_INTERVAL` (5s default):

```text
ActiveProjects()
      │
      ▼
metrics.Usecase.Current()  ──▶ domain.Compute(records, window)
      │
      ├──▶ SnapshotRepository.Save()
      ├──▶ Hub.BroadcastSnapshot()
      └──▶ anomaly.Usecase.Evaluate()
                 │
                 ├── ThresholdRepository.Get() (defaults when absent)
                 ├── domain.Detect(snapshot, thresholds)
                 ├── AnomalyRepository.HasRecent(type, dedupe window)
                 ├── Explainer.Explain() (Gemini or deterministic fallback)
                 ├── AnomalyRepository.Save()
                 └── Hub.BroadcastAnomaly()
```

Snapshots older than `SNAPSHOT_RETENTION` are pruned at most once an hour.
`ActiveProjects` also drops projects whose live window is empty, so idle
projects stop generating rows.

## 4. Backend structure

```text
apps/api/
  app/main.go                 composition root, lifecycle, healthcheck flag
  domain/                     entities, sentinel errors, ports, pure logic
    project.go threshold.go metric.go anomaly.go identity.go
    ports.go errors.go ids.go
  project/
    usecase.go
    repository/memory/
    repository/sqlite/
  threshold/
    usecase.go
    repository/{memory,sqlite}/
  metrics/
    usecase.go                 current snapshot, future range reads
    repository/memory/         live rolling request window
    repository/sqlite/         snapshots
  ingest/usecase.go
  anomaly/
    usecase.go                 evaluate + dedupe + explain + publish
    worker.go                  periodic pipeline
    repository/{memory,sqlite}/
  internal/
    config/                    env parsing and production guards
    httpx/                     JSON encode/decode, error mapping
    reqid/                     request id context
    middleware/                request id, real IP, logger, recoverer,
                               identity, admin, timeout
    rest/                      HTTP handlers per feature
    ws/                        hub + websocket handler
    llm/                       Gemini explainer with fallback
    sqlite/                    connection, pragmas, schema migration
    server/                    chi route map and middleware wiring
```

### Dependency rule

`domain` imports only the standard library. Feature usecases import `domain`
and define behaviour in terms of its ports. Adapters (`repository/*`,
`internal/llm`, `internal/ws`) implement those ports. Delivery
(`internal/rest`) depends on usecases, never on repositories. The composition
root is the only place that knows every concrete type.

The `anomaly` package does not import the `metrics` package. It consumes
`domain.SnapshotProvider`, which `metrics.Usecase` satisfies structurally. This
keeps feature modules independent.

### Domain logic

- `domain.ComputeAt` calculates RPS, nearest-rank P50/P95/P99, 5xx error rate,
  unique IPs, busiest-IP RPS, average payload, and per-endpoint counts.
- `domain.Detect` evaluates four rules. A threshold of 0 disables that rule:
  - `high_traffic` — RPS above limit
  - `high_error_rate` — 5xx share above limit
  - `high_latency` — P95 above limit
  - `possible_ip_flood` — busiest IP RPS above limit
- `domain.HashAPIKey` uses SHA-256. Keys are 32 random bytes rendered as hex;
  there is no low-entropy secret to brute-force.
- Project IDs are 8 random bytes (16 hex characters).

### Delivery middleware

Order for every request:

```text
RequestID → StripSlashes → RealIP → Logger → Recoverer → route
```

- The request ID is taken from `X-Request-ID` or generated, then returned in
  the response and included in error bodies.
- `RealIP` trusts `X-Forwarded-For`/`X-Real-IP` only when `TRUST_PROXY` is set.
- The logger's `statusWriter` forwards `Hijack` and `Flush`, so websocket
  upgrades survive logging.
- `Recoverer` logs a stack and writes a JSON 500.
- `/api` additionally gets `Timeout` (15s). `/ws` never does, because the
  connection is long-lived.
- `Time` limits, identity, and admin checks are route-group scoped.

## 5. Data model

SQLite runs with WAL, `busy_timeout=5000`, foreign keys, and
`SetMaxOpenConns(1)` to serialize writers.

```sql
projects (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  api_key_hash TEXT NOT NULL,
  created_at INTEGER NOT NULL
)

thresholds (
  project_id TEXT PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
  p95_ms REAL NOT NULL DEFAULT 1000,
  error_rate REAL NOT NULL DEFAULT 5,
  rps_max REAL NOT NULL DEFAULT 100,
  req_per_ip_max REAL NOT NULL DEFAULT 30
)

metric_snapshots (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id TEXT NOT NULL,
  ts INTEGER NOT NULL,              -- unix milliseconds
  window_s INTEGER NOT NULL,
  rps REAL, p50 REAL, p95 REAL, p99 REAL,
  error_rate REAL, unique_ips INTEGER,
  top_ip_rps REAL, avg_payload_bytes REAL,
  endpoints_json TEXT NOT NULL DEFAULT '{}'
)
-- index: (project_id, ts DESC)

anomalies (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id TEXT NOT NULL,
  type TEXT NOT NULL,
  severity TEXT NOT NULL,
  message TEXT NOT NULL,
  value REAL NOT NULL,
  threshold REAL NOT NULL,
  analysis TEXT NOT NULL DEFAULT '',
  detected_at INTEGER NOT NULL
)
-- indexes: (project_id, detected_at DESC),
--          (project_id, type, detected_at DESC)
```

`endpoints_json` is the only embedded document left: it is a variable-size map
with no query requirements. Everything used for filtering or ordering is a real
column.

### Migration

`internal/sqlite.Migrate` runs on startup and is idempotent:

1. Create `projects` and `thresholds` if absent.
2. If `metric_snapshots` has a `data_json` column, read the legacy rows, rename
   the table to `metric_snapshots_legacy_v1`, create the typed table, and copy
   parsed values.
3. If `anomalies` has `summary` and no `type`, do the same into
   `anomalies_legacy_v1`, splitting `"type: message"` summaries.
4. Drop the unused `alerts` table.
5. Set `PRAGMA user_version = 2`.

Legacy tables are retained for inspection but are not read by the application.

### Live records

Raw `LogRecord`s live in `metrics/repository/memory`, keyed by project. Each
`Append` and `Recent` prunes entries older than the rolling window. This bounds
memory by (active projects × request rate × window), not by uptime. A restart
loses the current window and the next snapshot starts fresh; history remains in
SQLite.

## 6. Auth and trust

Two independent mechanisms share the origin:

| Traffic | Mechanism | Identity |
| --- | --- | --- |
| `/api/*`, `/ws/*` | oauth2-proxy at the edge | email header |
| `/ingest/*` | `X-API-Key` in Go | project id |

The Go API has no login page, cookie, or session. It trusts headers only
because it is unreachable except through the edge. When
`PROXY_SHARED_SECRET` is set, it also requires `X-Proxy-Secret`, so a
misconfiguration that exposes the API port does not immediately expose the
control plane.

`AUTH_MODE`:

- `dev` — inject `DEV_USER_EMAIL`, mark the caller admin. Rejected when
  `APP_ENV=production`.
- `proxy` — require the email header and optional secret. This is the default
  in production.

`ADMIN_EMAILS` is compared case-insensitively. Empty means nobody is an admin:
the behavior is fail-closed, so at least one admin must be configured before
mutating routes become usable.

## 7. Realtime design

Envelope:

```json
{
  "event_type": "metric_snapshot",
  "project_id": "…",
  "timestamp": "2026-01-01T12:00:05Z",
  "data": { "rps": 12.4, "p95": 84.1, "...": "..." }
}
```

Event types are `metric_snapshot` and `anomaly_alert`.

The hub groups subscribers by project. Each client has:

- `send chan []byte` (buffer 64) — never closed.
- `done chan struct{}` — closed exactly once via `sync.Once`.

Broadcast copies the subscriber slice under `RLock`, then performs a
non-blocking send per client (`send`, `done`, `default`). A slow client drops
events instead of stalling the worker. Unregister closes `done`, removes the
client, and the writer goroutine exits. Because `send` is never closed, the
classic "send on closed channel" panic is structurally impossible.

The handler runs one reader goroutine (disconnect detection and pong deadlines)
and one writer loop (messages and pings). The HTTP server has no `WriteTimeout`
so long-lived websockets are not cut off.

## 8. SPA

```text
src/
  app.tsx                    routes: / and /p/:projectId
  main.tsx                   QueryClient + BrowserRouter
  types/api.ts               zod schemas mirroring Go JSON
  lib/api.ts                 typed fetch, 401 → /oauth2/start
  lib/live-store.ts          ring buffer + useSyncExternalStore
  lib/query.ts               TanStack Query defaults
  features/projects|thresholds|metrics/  REST clients
  features/live/connection.ts            reconnecting websocket
  components/charts/TimeSeries.tsx       uPlot lifecycle
  components/layout/AppShell.tsx         shell + identity
  routes/Dashboard.tsx
  routes/ProjectDetail.tsx
```

### Two data lanes

- **REST (TanStack Query)** — project list/detail, thresholds, anomaly history.
  Low frequency, benefits from caching and invalidation.
- **WebSocket** — snapshots and new anomaly alerts. High frequency.

Snapshots do **not** go into React Query. They are pushed into a per-project
ring buffer (`capacity = 900`) that exposes a cached snapshot object through
`useSyncExternalStore`. Notifications are coalesced with
`requestAnimationFrame`, so a burst of messages causes at most one React render
per frame. uPlot receives aligned arrays and updates its canvas imperatively;
React never maps over samples for rendering.

### Reconnect and session expiry

`LiveConnection` reconnects with exponential backoff (1s → 30s). After three
consecutive failures it probes `/api/auth/me`; a 401 redirects to
`/oauth2/start?rd=…` instead of retrying forever. The initial websocket message
is a full snapshot, so a reconnect immediately repopulates the chart.

## 9. Deployment

```text
docker compose
  edge          Caddy + built SPA, ports 80/443
  api           distroless static binary, volume /data
  oauth2-proxy  Google/OIDC, internal only
```

- `apps/api/Dockerfile` builds with `CGO_ENABLED=0` (modernc SQLite is pure Go)
  and runs on `distroless/static:nonroot`.
- The binary exposes `--healthcheck`, which calls `/health` on localhost. This
  works without a shell or curl in the runtime image.
- `deploy/Dockerfile.edge` builds the SPA with Bun, then copies `dist/` and the
  Caddyfile into the Caddy image.
- `compose.yaml` waits for the API healthcheck before starting the edge.
- Caddy sets HSTS, CSP, `X-Content-Type-Options`, a referrer policy, and a
  permissions policy. CSP allows `style-src 'unsafe-inline'` because uPlot and
  React inject style attributes; scripts remain `'self'`.
- The SPA fallback is `try_files {path} /index.html`, so deep links work.

## 10. Tradeoffs and limits

- **Single writer.** SQLite plus a process-local hub means one API instance.
  Scaling out requires a shared store and a pub/sub bus.
- **Synchronous explanation.** The worker calls the explainer inline; a Gemini
  call can add up to 4 seconds to a tick. The per-project rate limit (one call
  per 60s) and the deterministic fallback bound the damage. A queue would be
  the next step.
- **Memory window is per process.** A restart loses up to one window of raw
  requests, so the first snapshot after restart may be sparse.
- **Dedupe is per anomaly type.** A project can have at most one alert of each
  type per `DEDUPE_WINDOW`, regardless of severity changes.
- **No HTTP history endpoint yet.** `SnapshotRepository.Range` and
  `metrics.Usecase.History` exist, but the SPA currently builds history from
  the live stream. Adding `GET /api/projects/{id}/metrics/history` is small.
- **Anomaly list is capped** at 100 by default (1000 maximum) and is not
  paginated.
- **LLM output is untrusted text.** It is rendered as text, not HTML.

## 11. Testing

- `domain` — compute and detection are pure and table-testable.
- Feature usecases — memory repositories stand in for SQLite.
- Middleware — dev injection, proxy header requirement, shared secret, admin
  gate.
- `internal/sqlite` — migrates a synthetic v1 schema and verifies copied rows.
- `internal/server` — full HTTP lifecycle over `httptest`: create, ingest,
  metrics, thresholds, anomalies, auth, 404/405, websocket initial snapshot.
- `internal/ws` — concurrent register/broadcast/unregister under `-race`.

```bash
make check      # go test, go vet, tsc, vitest
make api-race   # go test -race ./...
make build      # static API binary + SPA bundle
```
