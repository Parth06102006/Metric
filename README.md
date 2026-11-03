# Metric

AI Traffic Command Center — a self-hosted API traffic observability service.

Ingest request logs with an API key, roll them into live metrics, detect
threshold breaches, and explain incidents. One Go API and one React SPA,
served from the same origin.

## Stack

| Layer | Choice |
| --- | --- |
| API | Go 1.27, chi, `log/slog`, SQLite via `modernc.org/sqlite` (no cgo) |
| Web | React 19, Vite, TypeScript, Tailwind CSS v4, TanStack Query, uPlot |
| Edge | Caddy + oauth2-proxy, Docker Compose, single VPS |

## Layout

```text
apps/api/                 Go API (clean architecture)
  app/                    composition root / main
  domain/                 entities, errors, ports, pure compute + detection
  project/                project usecase, lifecycle, memory/sqlite adapters
  threshold/              threshold usecase + adapters
  metrics/                current/history usecase + live memory + sqlite adapters
  ingest/                 ingest usecase
  anomaly/                evaluation usecase, worker, adapters
  audit/                  audit usecase, retention pruner, memory/sqlite adapters
  internal/               config, HTTP delivery, middleware, ws, llm, sqlite
apps/web/                 React SPA (independent Bun/Vite build)
  src/features/           REST clients, identity, websocket connection
  src/lib/                api, query, live ring buffer, clipboard, formatting
  src/components/         ui, chart, layout
  src/routes/             dashboard, project detail, audit log
simulator/simulator.py    local traffic generator (Python 3, stdlib only)
deploy/                   Caddyfile, edge Dockerfile, oauth2-proxy config
docs/architecture.md      design and request flows
compose.yaml              API + oauth2-proxy + edge, only 80/443 published
Makefile                  dev, test, build, deploy targets
```

## Local development

Prerequisites: Go 1.27.3 or newer (as specified by `apps/api/go.mod`), Node.js
22.19+ (Node 24 is used in CI), Bun 1.4.4+, and Python 3 for the optional
simulator. Bun 1.4.4 is required for the scoped dependency override and lockfile.
Go may download the required toolchain automatically when `GOTOOLCHAIN=auto`.
For browser E2E tests also install Playwright Chromium as described below.

From the repository root in a Linux/WSL shell:

```bash
make install
ADDR=127.0.0.1:8080 APP_ENV=development AUTH_MODE=dev \
  OPENROUTER_API_KEY= make dev
```

Open <http://localhost:5173>. Vite proxies `/api`, `/ingest`, and `/ws` to the
API, so the browser sees one origin and no CORS is needed. Stop with Ctrl-C.
Ensure ports 8080 and 5173 are free. The Go API reads environment variables; it
does **not** automatically load `.env.local` or `.env.example` for `make dev`.
The SQLite database defaults to `apps/api/traffic.db` when run through Make.

In development `AUTH_MODE=dev` injects `DEV_USER_EMAIL` (default
`dev@localhost`) as an **admin** without authentication. Bind to loopback and
never expose this mode to an untrusted network; it is rejected in production.
An empty `OPENROUTER_API_KEY` gives rule-only alerts and does not call Jev. To test
Jev, export your OpenRouter key in the shell before starting the API (omit the empty
assignment above); provider usage can incur charges. Do not commit the key.
If migrating from the direct TypeSafe integration, replace `TYPESAFE_API_KEY`
with an **OpenRouter** key in `OPENROUTER_API_KEY` and update any old `JEV_MODEL`
value to an OpenRouter model ID such as `typesafe/jev-1.14`. TypeSafe keys are
not forwarded to OpenRouter.

Run each side separately with `make api-dev` and `make web-dev`.

## Creating a project and ingesting

1. Open the dashboard and create a project. The API key is shown once.
2. Send a batch:

```bash
curl -X POST http://localhost:8080/ingest/<PROJECT_ID> \
  -H 'Content-Type: application/json' \
  -H 'X-API-Key: <API_KEY>' \
  -d '{
    "logs": [
      {
        "ts": "'"$(date -u +%Y-%m-%dT%H:%M:%SZ)"'",
        "endpoint": "/checkout",
        "method": "POST",
        "status": 200,
        "latency_ms": 25,
        "ip": "203.0.113.7",
        "payload_bytes": 512
      }
    ]
  }'
```

Batches accept 1–1000 records, capped at 1 MiB. Every 5 seconds the worker
computes a snapshot for each active project, persists it, broadcasts it on the
project websocket, and runs the four anomaly rules (RPS, error rate, P95,
per-IP rate). Snapshots older than `SNAPSHOT_RETENTION` are pruned hourly.

When `OPENROUTER_API_KEY` is configured, each new alert and a bounded analysis
job are saved together in SQLite. A background worker asks Jev four independent
questions in one request; the alert is not blocked by the provider. Its
analysis remains advisory and never changes alert severity. Without a key,
alerts use rule-based text. An unavailable provider or a full queue leaves
the rule-based alert available; the analysis status is included in the API.
The worker makes at most one provider attempt every 30 seconds per process;
large incident bursts may therefore remain pending in the queue.
The provider and stored diagnosis receive aggregate metrics, an anonymous
endpoint summary, and (when available) a baseline from older snapshots; no raw
IP addresses, paths, request bodies or project keys are sent. The client uses
the OpenRouter Decisions API and requires confidence fields and token usage in
the response; if these optional provider fields are missing, it preserves the
rule-based alert rather than inventing values. Baseline span and sample
count describe the available snapshots, not continuous coverage. Suggestions
are withheld unless the incident has at least 20 requests, a baseline with at
least 12 snapshots spanning 10 minutes, and a model evidence score of at least
0.7; these are initial heuristics, not validated accuracy guarantees. Endpoint
investigation additionally requires observed endpoint errors. Old queued jobs
without sample data also use the insufficient-evidence fallback.

The live request window is still volatile, and crash recovery may repeat a
provider request after a completed response was not yet stored. The interval
limit is process-local, resets on restart, and is not a monetary spending cap.
Analysis quality and provider integration should be evaluated on real labeled
incidents before relying on the recommendations.

To run the opt-in provider integration test, set an OpenRouter key in your shell
without pasting it into a command or the terminal history, then run:

```bash
read -rs -p 'OpenRouter API key: ' OPENROUTER_API_KEY; echo
export OPENROUTER_API_KEY
cd apps/api
JEV_LIVE_TEST=1 go test -v -count=1 ./internal/analysis -run '^TestLiveJevIncidentAnalysis$'
unset OPENROUTER_API_KEY
```

This sends **one potentially billable request** containing synthetic aggregate
metrics to Jev via OpenRouter's Decisions API, verifies a diagnosis is persisted in a temporary
SQLite database, and reports latency and input tokens. It does not test a real
incident's quality or the entire browser/WebSocket path. It is skipped in
ordinary test and CI runs. `JEV_MODEL` optionally selects another model.

SQLite uses up to **10 connections** per API process. The normal file-backed
database uses WAL, a 5-second busy timeout and foreign keys on each connection;
explicit write transactions acquire the writer lock before reading. SQLite
still permits only one writer at a time: a larger pool mainly allows concurrent
reads, not ten concurrent writers. Once the pool is full, `database/sql` waits
on a channel until a connection is returned or the request context is canceled;
SQLite lock waits are a separate mechanism. Long transactions or contention can
still produce busy errors. The WAL and timeout may differ when supplying a
custom SQLite URI in `DATABASE_PATH`.

To generate traffic locally:

```bash
python3 simulator/simulator.py \
  --project-id <PROJECT_ID> --api-key <API_KEY> \
  --rps 20 --duration 120
```

Add `--spike`, `--errors`, or `--flood` (one at a time) to trigger a latency
spike, an error-rate incident, or an IP flood. The script uses only the Python
standard library and posts batches to `/ingest/<PROJECT_ID>`.

## Roles and ownership

The API trusts the identity asserted by the edge and compares the caller's email
with `ADMIN_EMAILS` on every request. Two roles follow from that:

- **Admin** — creates projects, assigns owners and viewers, rotates and revokes
  ingest keys, edits thresholds, deletes projects, and reads the audit log.
- **Everyone else** — has read-only access to projects they own or have been
  added to as a viewer.

A user appears in the admin-only directory after their first authenticated
`/api/auth/me` request (normally made by the web client after sign-in). This is
not an external identity-provider directory: users must sign in before they can
be added. Admins manage viewers in Project access. Removing a viewer disconnects
project streams in this process; authorized users may reconnect. An existing
owner remains authorized even if removed from the separate viewer list.

Existing owners are preserved. Legacy projects with no owner are admin-only
until an owner or viewer is added. Use `PATCH /api/projects/{id}` with
`{"owner_email": "person@example.com"}` or the Ownership card to change the owner.
Clearing the owner does not remove any separately granted viewers.

`ADMIN_EMAILS` is fail-closed: when it is empty nobody is an admin, so project
creation, key management, thresholds, deletion, owner assignment, and the audit
feed are all closed. Upgrading an existing deployment requires setting it before
those routes can be used.

## OpenAPI and generated React validation

`apps/api/openapi.yaml` is the manually maintained OpenAPI 3.0 contract for the
HTTP surface and WebSocket message payloads. Hey API reads it using
`apps/web/openapi-ts.config.ts` and generates TypeScript types and Zod schemas
in `apps/web/src/generated/api/`. The existing React fetch wrapper parses REST
responses with generated operation schemas; outgoing project and threshold
bodies and incoming WebSocket messages are also validated. The Go server keeps
its own validation and authorization; generated client validation is not a
security boundary and does not replace server-side checks. No generated HTTP
SDK or Swagger UI is currently served by the API.

When a wire contract changes, update the Go handlers/types and OpenAPI together,
then run:

```bash
make api-contract-generate   # validate the spec and regenerate checked-in files
make api-contract-check      # validate and compare against a clean regeneration
make web-test
make web-e2e                 # exercises real HTTP responses and browser flows
```

Commit the changed spec and generated files together. CI checks generation
drift and exercises the tested HTTP operations against the real Go API; this is
not an automatic proof that every future handler change matches the spec.

## HTTP surface

| Method | Path | Auth |
| --- | --- | --- |
| `GET` | `/health` | public |
| `POST` | `/ingest/{projectID}` | `X-API-Key` |
| `GET` | `/api/auth/me` | proxy identity |
| `GET` | `/api/users` | admin; users seen after sign-in |
| `GET`, `POST`, `DELETE` | `/api/projects/{projectID}/viewers` | admin; manage view-only access |
| `GET` | `/api/projects` | proxy identity |
| `POST` | `/api/projects` | proxy identity + admin |
| `GET` | `/api/projects/{projectID}` | proxy identity (owner or admin) |
| `PATCH` | `/api/projects/{projectID}` | proxy identity + admin |
| `GET` | `/api/projects/{projectID}/thresholds` | proxy identity (owner or admin) |
| `PUT` | `/api/projects/{projectID}/thresholds` | proxy identity + admin |
| `GET` | `/api/projects/{projectID}/metrics` | proxy identity (owner or admin) |
| `GET` | `/api/projects/{projectID}/anomalies` | proxy identity (owner or admin) |
| `POST` | `/api/projects/{projectID}/api-key` | proxy identity + admin |
| `DELETE` | `/api/projects/{projectID}/api-key` | proxy identity + admin |
| `DELETE` | `/api/projects/{projectID}` | proxy identity + admin |
| `GET` | `/api/audit` | proxy identity + admin |
| `GET` | `/ws/{projectID}` | proxy identity at handshake |

`GET /api/projects` and `GET .../anomalies` are additive to the original v1
surface; the SPA needs them for the project list and anomaly history.

### Security audit log

`GET /api/audit` exposes an append-only, admin-only feed of security events
(project create, owner assignment, delete, key rotate/revoke, threshold updates,
auth and authorization denials including cross-user REST reads and WebSocket
attempts). Records carry the actor, action, target project, outcome, request ID,
and a UTC timestamp; credential-shaped detail fields are redacted before
storage. There are no endpoints that update or delete audit records. A failed
audit write never blocks the primary operation: the event is emitted at ERROR to
the structured application log instead, so audit gaps are possible only while
the database is unavailable — monitor that log for `audit record write failed`.
Events older than `AUDIT_RETENTION` are pruned hourly.

The feed is newest-first and accepts:

| Parameter | Meaning |
| --- | --- |
| `limit` | Page size, capped at 1000 (default 100) |
| `action` | Exact action, e.g. `access.denied` |
| `project_id` | Exact target project |
| `before` + `before_id` | Exclusive cursor from the last event on screen; both are returned by the previous page, so events that share a millisecond are never skipped |

A malformed `before` or `before_id` returns 400. Anything the caller is not
allowed to read stays a 404 in the response, while the denial is recorded as
`access.denied` for administrators.

## Deploy

The edge owns TLS and SSO. Only `edge` publishes ports.

```bash
cp .env.example .env
# fill SITE_ADDRESS, ACME_EMAIL, oauth2-proxy client values
openssl rand -base64 32   # OAUTH2_PROXY_COOKIE_SECRET
openssl rand -hex 32      # PROXY_SHARED_SECRET

docker compose up -d --build
```

oauth2-proxy redirect URI: `https://<SITE_ADDRESS>/oauth2/callback`.

Routing:

- `/ingest/*` and `/health` go straight to the API. Machine clients cannot do
  an SSO redirect, so ingest authenticates with `X-API-Key`.
- `/api/*` and `/ws/*` use Caddy `forward_auth` against
  `oauth2-proxy:4180/oauth2/auth`. Caddy forwards
  `X-Auth-Request-Email`/`X-Auth-Request-User`.
- Client-supplied `X-Auth-Request-*` and `X-Proxy-Secret` headers are stripped
  at the edge before the auth check, so a browser or script can never assert
  an identity; only the verified SSO session defines one. If the session
  yields no identity, the headers stay absent and the API fails closed (401).
- `/oauth2/*` goes to oauth2-proxy.
- Everything else serves the built SPA with an `index.html` fallback.
- The API verifies `X-Proxy-Secret` when `PROXY_SHARED_SECRET` is set, so the
  headers are only trusted from the edge.

The API and oauth2-proxy have no published ports. Caddy adds HSTS, CSP,
`X-Content-Type-Options`, and a restrictive referrer policy.

### Sessions and offboarding

The session cookie is `Secure`, `HttpOnly`, `SameSite=Lax`, scoped to the site
root, and owned entirely by oauth2-proxy — the Go API never sees it. The
cookie lives 7 days (`cookie_expire`) and is revalidated with the identity
provider every hour (`cookie_refresh`): a user disabled or removed at the
provider is locked out at the next refresh or sign-in, and signing out via
`/oauth2/sign_out` clears the session immediately. Administrator rights are
evaluated by the Go API on every request against `ADMIN_EMAILS`, so removing
an email from the allowlist revokes that user's API admin capabilities at
their next request — no session changes required.

## Configuration

| Variable | Default | Purpose |
| --- | --- | --- |
| `ADDR` | `:8080` | API listen address (`PORT` also works) |
| `DATABASE_PATH` | `traffic.db` | SQLite file |
| `APP_ENV` | `development` | `production` enables guards |
| `AUTH_MODE` | derived | `dev` or `proxy`; defaults to `proxy` in production |
| `AUTH_EMAIL_HEADER` | `X-Auth-Request-Email` | trusted identity header |
| `AUTH_USER_HEADER` | `X-Auth-Request-User` | trusted username header |
| `PROXY_SHARED_SECRET` | empty | optional shared secret from Caddy |
| `DEV_USER_EMAIL` | `dev@localhost` | injected identity in dev |
| `ADMIN_EMAILS` | empty | comma-separated allowlist; empty means nobody is an admin (fail-closed: admin routes are denied for everyone) |
| `TRUST_PROXY` | `true` | read `X-Forwarded-For` / `X-Real-IP` |
| `ALLOWED_ORIGINS` | empty | extra websocket origins beyond same-host |
| `METRICS_WINDOW` | `60s` | rolling window for live metrics |
| `WORKER_INTERVAL` | `5s` | snapshot/detection cadence |
| `DEDUPE_WINDOW` | `60s` | suppress repeated anomaly types |
| `SNAPSHOT_RETENTION` | `168h` | snapshot retention |
| `AUDIT_RETENTION` | `2160h` | audit-event retention; an hourly job prunes older rows |
| `API_TIMEOUT` | `15s` | per-request timeout for `/api` |
| `OPENROUTER_API_KEY` | empty | enables Jev analysis through OpenRouter; rule-based alerts otherwise |
| `JEV_MODEL` | `typesafe/jev-1.14` | OpenRouter model identifier |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |

## Pre-commit checks

Install `pre-commit` (for example, `uv tool install pre-commit` or
`pipx install pre-commit`), install the project dependencies with `make install`,
then enable the Git hook:

```bash
pre-commit install
pre-commit run --all-files  # optional: check all tracked files
```

The hook checks file hygiene, Go formatting, vet and golangci-lint, frontend
typechecking, and OpenAPI generation when relevant files change. It requires the
Go, Node.js and Bun versions listed above. The first run downloads pinned hook
dependencies and builds golangci-lint v2.15.0 using Go 1.27.3; update the hook's
Go version alongside `apps/api/go.mod` when the minimum Go version changes.
Formatting hooks may modify files and stop the commit; review and stage those
changes before committing again. Full tests, browser tests and network-dependent
security scans remain in CI.

To run the Go linter independently, install golangci-lint v2.15.0 using its
[official installation instructions](https://golangci-lint.run/docs/welcome/install/)
and run `make api-lint` (also included in `make check`). The configuration in `.golangci.yml` enables the
standard linters. It excludes unchecked `Close` errors in test files only;
production error checks remain enabled.

## Make targets

```text
make help           list targets
make install        install Go modules and web dependencies
make dev            run API + web dev servers
make api-test       go test ./... (includes integration tests)
make api-integration  run API integration tests alone
make api-race       go test -race ./...
make api-vet        go vet ./...
make api-lint       golangci-lint on the API
make api-vuln       scan reachable Go vulnerabilities
make web-audit      audit production and development dependencies
make security-check run both vulnerability scans (network required)
make web-typecheck  tsc --noEmit
make web-test       vitest
make web-e2e        build the SPA and run Playwright browser tests
make perf-smoke     run a short isolated k6 smoke test
make perf-load      run the configurable isolated k6 load test
make check          tests + vet + lint + typecheck
make build          API binary + SPA bundle
make up / down      docker compose lifecycle
make logs           follow stack logs
```

GitHub Actions runs on pushes, pull requests, weekly, and manual dispatch.
Parallel jobs test, vet-check, lint, race-check and build the API, and test and build
the web app. CI also runs `govulncheck` and a full Bun dependency audit; both
need network access and fail when matching vulnerabilities are reported.
The API suite includes tests for trusted-proxy and ingest-key authentication,
HTTP input validation, WebSocket authentication and origin checks, owner changes,
plus an integration test across ingest, SQLite, incident analysis and WebSocket
delivery. It also includes failure/recovery tests. The transient
provider recovery test uses the real 30-second retry rate limit. These tests use
fake providers and need no API keys or external services.

`make web-test` runs Vitest unit tests and isolated React component tests with
React Testing Library. Component tests use a fresh query cache and router and
mock APIs, WebSockets, and the chart renderer where appropriate; they do not
replace the real browser/API integration tests. A scoped Bun override temporarily
upgrades the Hey API generator's `js-yaml` dependency to the patched 4.3.2;
remove it after the upstream parser upgrades its pinned dependency.

For browser tests, install Playwright Chromium once and run:

```bash
cd apps/web && bun install --frozen-lockfile && bunx playwright install chromium
cd ../.. && make web-e2e
```

The browser suite starts its own API with a temporary SQLite database and no AI
provider key, then serves the production SPA on local ports 18080 and 15173.
Both ports must be free. It tests project creation, ingestion, incident display,
and WebSocket recovery; it does not contact the real AI provider. Playwright
failure traces and screenshots are saved under `apps/web/test-results/`.

### k6 load testing

Install [k6](https://grafana.com/docs/k6/latest/set-up/install-k6/) v2.3.0 and
run `make perf-smoke` or `make perf-load` (Python 3 and Go are also required).
The launcher builds a loopback-only API with a temporary SQLite database,
unique development identity and no provider credentials; it removes the process
and database afterward. It never targets an existing API. The test creates a
disposable project, ingests synthetic error records, reads metrics, checks
WebSocket snapshots and verifies a single persisted incident.

The smoke profile uses two API VUs and one WebSocket VU for 10 seconds. The
default load profile ramps to 15 API VUs over five seconds, holds for 20 seconds
and ramps down for five, with two WebSocket VUs. Each API iteration sends a
five-record batch and sleeps 200 ms. For example:

```bash
METRIC_K6_LOAD_VUS=25 METRIC_K6_HOLD_SECONDS=60 make perf-load
mkdir -p load-test-results
METRIC_K6_SUMMARY="$PWD/load-test-results/load.json" make perf-load
METRIC_K6_P95_MS=100 make perf-smoke  # optional ingest p95 budget in milliseconds
```

`METRIC_K6_RAMP_SECONDS`, `METRIC_K6_WS_VUS`, `METRIC_K6_BATCH_SIZE` and
`METRIC_K6_SLEEP_MS` also configure the load; `K6_BIN` can point to the k6
executable. k6 fails on failed HTTP/checks, missing WebSocket snapshots or an
incorrect incident count. No latency budget is assumed by default. It reports
HTTP latency and throughput, WebSocket success and first-snapshot latency,
incident-detection time, and duplicate count. CI runs the smoke profile and
uploads its JSON summary. Results from a local, rule-only single-process test are
not production capacity or an SLO; restart recovery, provider calls, browser
rendering and host resource monitoring are not covered by this suite.

## Notes

- Raw request logs stay in memory for the rolling window; only computed
  snapshots and anomalies are persisted. Project charts offer 15-minute and
  one-hour wall-clock views in the browser's local timezone, backed by at most
  the newest 900 snapshots. At faster sampling rates or after retention/pruning,
  the selected range may not be fully covered. Each point is a rolling-window
  measurement, not an event count for the interval between points; percentiles
  are not averaged together. Latency and error rates are unavailable when the
  sampled window has no requests. To avoid drawing through long missing periods,
  charts break lines between samples separated by more than twice their rolling
  window (at least 60 seconds). A connection may be live while no new snapshots
  are produced; the latest sample time is displayed separately.
- A v1 database is upgraded in place. Old JSON-blob snapshot/anomaly tables are
  renamed `*_legacy_v1`, their rows are copied into the new typed tables, and
  the unused `alerts` table is dropped. The upgrade also adds `owner_email` and
  `api_key_revoked` with safe defaults, so pre-ownership projects become
  admin-only until an owner is assigned.
- Revoking an ingest key takes effect immediately: `/ingest` returns 401 until a
  new key is rotated. When ownership changes, existing project WebSockets are
  disconnected; authorized users can reconnect under the new permissions.
- The websocket hub never closes a client's send channel. Each subscriber has a
  `done` channel plus `sync.Once`, which removes the send-on-closed-channel
  race while keeping broadcasts non-blocking.
