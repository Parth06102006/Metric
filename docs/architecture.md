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
5. Routes that mutate or expose cross-tenant material add `RequireAdmin`:
   project creation, owner assignment, key rotate/revoke, threshold updates,
   project deletion, and the audit feed. With an empty `ADMIN_EMAILS`, nobody is
   an admin: the behavior is fail-closed.
6. Project-scoped reads check `project.CanAccess(identity)` in the usecase. A
   caller who is not the owner and not an admin gets `domain.ErrAccessDenied`,
   which delivery maps to the same 404 as a missing project, so responses never
   reveal whether a project exists. The handler still records an
   `access.denied` audit event.
7. The handler decodes JSON, calls one usecase, and writes the result. Domain
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
2. The Go handler checks the caller is an owner, viewer, or admin. An unauthorized
   handshake gets the same 404 as a missing project. After upgrading, it rechecks
   access before registration to close a race with access changes or deletion.
3. It immediately writes a snapshot computed from the current window, then
   forwards hub messages until the client disconnects, its read deadline
   expires, access changes, or the project is deleted. Ownership changes and
   viewer removals disconnect all subscribers; authorized clients may reconnect.
4. A process-local stream fence coordinates registration and writes with access
   changes and deletion. These mutations may wait for an in-progress WebSocket
   write (up to its 5-second deadline); this is not a multi-process fence.
5. The hub sends a ping every 30 seconds; the read deadline is 60 seconds.

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
                 ├── save rule-based anomaly + bounded job transactionally
                 └── Hub.BroadcastAnomaly()
                            │
                            ▼
                background worker → Jev → save advisory result → broadcast update
```

Snapshots older than `SNAPSHOT_RETENTION` are pruned at most once an hour.
`ActiveProjects` also drops projects whose live window is empty, so idle
projects stop generating rows. A separate hourly job prunes audit events older
than `AUDIT_RETENTION`.

## 4. Backend structure

```text
apps/api/
  app/main.go                 composition root, lifecycle, healthcheck flag
  domain/                     entities, sentinel errors, ports, pure logic
    project.go threshold.go metric.go anomaly.go identity.go
    ports.go errors.go ids.go
  project/
    usecase.go                 read/create with ownership checks
    lifecycle.go               rotate/revoke/delete/assign owner
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
    usecase.go                 evaluate + dedupe + queue + publish
    worker.go                  periodic pipeline
    repository/{memory,sqlite}/
  audit/
    usecase.go                 record (redacted) + filtered reads
    pruner.go                  hourly retention job
    repository/{memory,sqlite}/
  internal/
    config/                    env parsing and production guards
    httpx/                     JSON encode/decode, error mapping
    reqid/                     request id context
    middleware/                request id, real IP, logger, recoverer,
                               identity, admin, timeout
    rest/                      HTTP handlers per feature
    ws/                        hub + websocket handler
    jev/                       Jev structured incident analysis via OpenRouter
    analysis/                  durable background job worker
    sqlite/                    connection, pragmas, schema migration
    server/                    chi route map and middleware wiring
```

### Dependency rule

`domain` imports only the standard library. Feature usecases import `domain`
and define behaviour in terms of its ports. Adapters (`repository/*`,
`internal/ws`) implement those ports. Delivery
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
- `domain.ValidateEmail` shape-checks owner emails (empty means no owner) and
  `domain.SanitizeActor` lowercases/normalizes every recorded actor.

### Audit log

`domain.AuditEvent` is the single record shape: id, UTC timestamp, actor email,
action, target project, outcome, request ID, and a string map of curated
details. Actions are constants (`project.create`, `project.owner.assign`,
`api_key.rotate`, `access.denied`, …) so the feed stays filterable and no
handler invents free-form strings.

- Write path: `domain.AuditSink` is the minimal port. `audit.Usecase` implements
  it, redacts credential-shaped detail keys (`domain.RedactDetails`), stamps the
  request ID, and logs a write failure at ERROR without failing the request. A
  nil sink is a no-op, so delivery never depends on the audit subsystem being
  wired.
- Read path: `AuditRepository.List` takes `domain.AuditQuery` (limit, action,
  project, composite `before`/`before_id` cursor). The usecase clamps the limit;
  the REST handler validates the cursor and returns 400 on malformed input.
- Retention: `audit.Pruner` deletes rows older than `AUDIT_RETENTION` at most
  once an hour. There is intentionally no HTTP route that updates or deletes
  audit rows.

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
  created_at INTEGER NOT NULL,
  owner_email TEXT NOT NULL DEFAULT '',      -- '' = no owner (viewers may be granted separately)
  api_key_revoked INTEGER NOT NULL DEFAULT 0 -- 1 rejects ingest until rotation
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

audit_events (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  ts INTEGER NOT NULL,                 -- unix milliseconds
  actor_email TEXT NOT NULL DEFAULT '',
  action TEXT NOT NULL,
  target_project_id TEXT NOT NULL DEFAULT '',
  outcome TEXT NOT NULL,               -- success | failure
  request_id TEXT NOT NULL DEFAULT '',
  details_json TEXT NOT NULL DEFAULT '{}'
)
-- indexes: (ts DESC), (action, ts DESC)
```

`endpoints_json` is the only embedded document left: it is a variable-size map
with no query requirements. Everything used for filtering or ordering is a real
column.

### Migration

`internal/sqlite.Migrate` runs on startup and is idempotent:

1. Create `projects` and `thresholds` if absent.
2. Add `owner_email` and `api_key_revoked` to an existing `projects` table.
   Existing rows default to `''` and `0`, so pre-ownership projects stay
   admin-only until an owner is assigned.
3. If `metric_snapshots` has a `data_json` column, read the legacy rows, rename
   the table to `metric_snapshots_legacy_v1`, create the typed table, and copy
   parsed values.
4. If `anomalies` has `summary` and no `type`, do the same into
   `anomalies_legacy_v1`, splitting `"type: message"` summaries.
5. Create the snapshot, anomaly, and audit tables with their indexes.
6. Drop the unused `alerts` table.
7. Add diagnosis fields and durable analysis jobs.
8. Add the authenticated user directory and view-only project memberships; set
   `PRAGMA user_version = 7`.

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

Project-level authorization is separate from admin rights. `project.CanAccess`
grants access to the owner, explicitly granted viewers, or an admin. Viewer
membership is independent of the existing owner, so clearing an owner does not
remove viewers. Unauthorized reads return 404 rather than 403, and each denial
is recorded as `access.denied`. The admin-only known-user directory is populated
only after trusted identity reaches `/api/auth/me`, not from arbitrary input.

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
  app.tsx                    routes: /, /p/:projectId, /audit
  main.tsx                   QueryClient + BrowserRouter
  types/api.ts               zod schemas mirroring Go JSON
  lib/api.ts                 typed fetch, 204 handling, 401 → /oauth2/start
  lib/live-store.ts          ring buffer + useSyncExternalStore
  lib/query.ts               TanStack Query defaults
  features/identity/         /api/auth/me + shared useMe() hook
  features/projects/         project lifecycle REST clients
  features/thresholds|metrics/  REST clients
  features/audit/            audit feed client + query builder
  features/live/connection.ts    reconnecting websocket
  components/charts/TimeSeries.tsx  uPlot lifecycle
  components/layout/AppShell.tsx    shell, identity, admin nav
  routes/Dashboard.tsx
  routes/ProjectDetail.tsx
  routes/AuditLog.tsx
```

Admin-only surfaces are hidden, not just disabled: the dashboard drops the
"New project" control, the project page only renders the key/ownership/delete
cards for admins, and the audit route explains that it is restricted instead of
fetching (the API would answer 403). The server remains the authority for every
one of these actions.

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
- **Asynchronous advisory analysis.** With an OpenRouter key, a bounded SQLite
  queue and one background goroutine enrich persisted alerts using Jev. Jobs are leased,
  retried at most three times and can be replayed after a crash; a provider
  call may be billed again if a crash happens before the result is committed.
  Queue-full and failed cases preserve the rule-based alert. Without a key,
  no jobs are created or processed; existing pending jobs resume when the key
  is restored. The provider receives aggregate metrics and anonymized endpoint
  summaries, not IPs or paths. Completed diagnoses retain that evidence.
  Baseline snapshots are selected before the incident window and summarized
  using medians; their time span does not guarantee continuous coverage.
  Suggestions require a baseline, at least 20 requests and a model evidence
  probability of 0.7; these initial gates are heuristics, not measured accuracy.
  The 30-second attempt limit is per process and is not a spending cap.
- **Memory window is per process.** A restart loses up to one window of raw
  requests, so the first snapshot after restart may be sparse.
- **Dedupe is per anomaly type.** A project can have at most one alert of each
  type per `DEDUPE_WINDOW`, regardless of severity changes.
- **Graph history is bounded to the newest 900 snapshots.** The SPA restores
  this series from `GET /api/projects/{id}/metrics/history` on load and after
  reconnect, then merges it with the live stream. `SnapshotRepository.Range`
  remains the time-range query used by analysis.
- **Anomaly API results are capped** at 100 by default (1000 maximum); the
  dashboard paginates only the loaded recent 100, not the full historical set.
- **Audit writes are best-effort.** If the database is unavailable, the event is
  written to the structured log instead and the primary request still succeeds.
  Monitor `audit record write failed` for gaps.
- **Audit reads are exact-match.** Action, project, limit, and a composite cursor
  are supported; there is no full-text search over details.
- **Identity is email-based.** The local directory records emails observed at
  trusted sign-in, not a full identity-provider directory. Renaming an account
  requires updating ownership and viewer membership.
- **Model output is advisory.** Closed-set answers are validated, and confidence
  is not proof of correctness. The narrative is rendered as text, not HTML.

## 11. Testing

- `domain` — compute and detection are pure and table-testable.
- Feature usecases — memory repositories stand in for SQLite, including the
  audit adapter used by its usecase and pruner tests.
- `audit` — redaction, non-blocking write failure, limit clamping, and
  action/project/cursor filters.
- Middleware — dev injection, proxy header requirement, shared secret, admin
  gate, fail-closed empty admin list.
- `internal/sqlite` — migrates a synthetic v1 schema and verifies copied rows,
  ownership defaults, and revocation defaults.
- `internal/server` — full HTTP lifecycle over `httptest`: create, ingest,
  metrics, thresholds, anomalies, ownership assignment, key rotate/revoke,
  deletion, audit filters and cursor validation, auth, 404/405, websocket
  initial snapshot, cross-user REST/WebSocket denials, and stream shutdown on
  delete.
- `internal/ws` — concurrent register/broadcast/unregister under `-race`.

```bash
make check      # go test, go vet, tsc, vitest
make api-race   # go test -race ./...
make build      # static API binary + SPA bundle
```
