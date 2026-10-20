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
  project/                project usecase + memory/sqlite adapters
  threshold/              threshold usecase + adapters
  metrics/                current/history usecase + live memory + sqlite adapters
  ingest/                 ingest usecase
  anomaly/                evaluation usecase, worker, adapters
  internal/               config, HTTP delivery, middleware, ws, llm, sqlite
apps/web/                 React SPA (independent Bun/Vite build)
  src/features/           API clients and websocket connection
  src/lib/                api, query, live ring buffer, formatting
  src/components/         ui, chart, layout
  src/routes/             dashboard and project detail
deploy/                   Caddyfile, edge Dockerfile, oauth2-proxy config
docs/architecture.md      design and request flows
compose.yaml              API + oauth2-proxy + edge, only 80/443 published
Makefile                  dev, test, build, deploy targets
```

## Local development

Prerequisites: Go 1.27+, Bun 1.3+.

```bash
make install   # go mod download + bun install
make dev       # API on :8080, Vite on :5173
```

Open <http://localhost:5173>. Vite proxies `/api`, `/ingest`, and `/ws` to the
API, so the browser sees one origin and no CORS is needed.

In development `AUTH_MODE` defaults to `dev`, which injects
`DEV_USER_EMAIL` (default `dev@localhost`). `AUTH_MODE=dev` is rejected when
`APP_ENV=production`.

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
        "ts": "2026-01-01T12:00:00Z",
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

## HTTP surface

| Method | Path | Auth |
| --- | --- | --- |
| `GET` | `/health` | public |
| `POST` | `/ingest/{projectID}` | `X-API-Key` |
| `GET` | `/api/auth/me` | proxy identity |
| `GET` | `/api/projects` | proxy identity |
| `POST` | `/api/projects` | proxy identity + admin |
| `GET` | `/api/projects/{projectID}` | proxy identity |
| `GET` | `/api/projects/{projectID}/thresholds` | proxy identity |
| `PUT` | `/api/projects/{projectID}/thresholds` | proxy identity + admin |
| `GET` | `/api/projects/{projectID}/metrics` | proxy identity |
| `GET` | `/api/projects/{projectID}/anomalies` | proxy identity |
| `GET` | `/ws/{projectID}` | proxy identity at handshake |

`GET /api/projects` and `GET .../anomalies` are additive to the original v1
surface; the SPA needs them for the project list and anomaly history.

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
- `/oauth2/*` goes to oauth2-proxy.
- Everything else serves the built SPA with an `index.html` fallback.
- The API verifies `X-Proxy-Secret` when `PROXY_SHARED_SECRET` is set, so the
  headers are only trusted from the edge.

The API and oauth2-proxy have no published ports. Caddy adds HSTS, CSP,
`X-Content-Type-Options`, and a restrictive referrer policy.

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
| `ADMIN_EMAILS` | empty | comma-separated allowlist; empty means all authenticated users |
| `TRUST_PROXY` | `true` | read `X-Forwarded-For` / `X-Real-IP` |
| `ALLOWED_ORIGINS` | empty | extra websocket origins beyond same-host |
| `METRICS_WINDOW` | `60s` | rolling window for live metrics |
| `WORKER_INTERVAL` | `5s` | snapshot/detection cadence |
| `DEDUPE_WINDOW` | `60s` | suppress repeated anomaly types |
| `SNAPSHOT_RETENTION` | `168h` | snapshot retention |
| `API_TIMEOUT` | `15s` | per-request timeout for `/api` |
| `GEMINI_API_KEY` | empty | enables LLM explanations; fallback otherwise |
| `GEMINI_MODEL` | `gemini-2.0-flash` | Gemini model |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |

## Make targets

```text
make help           list targets
make install        install Go modules and web dependencies
make dev            run API + web dev servers
make api-test       go test ./...
make api-race       go test -race ./...
make api-vet        go vet ./...
make web-typecheck  tsc --noEmit
make web-test       vitest
make check          tests + vet + typecheck
make build          API binary + SPA bundle
make up / down      docker compose lifecycle
make logs           follow stack logs
```

## Notes

- Raw request logs stay in memory for the rolling window; only computed
  snapshots and anomalies are persisted.
- A v1 database is upgraded in place. Old JSON-blob snapshot/anomaly tables are
  renamed `*_legacy_v1`, their rows are copied into the new typed tables, and
  the unused `alerts` table is dropped.
- The websocket hub never closes a client's send channel. Each subscriber has a
  `done` channel plus `sync.Once`, which removes the send-on-closed-channel
  race while keeping broadcasts non-blocking.
