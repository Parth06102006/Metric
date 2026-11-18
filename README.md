<p align="center">
  <img src="docs/metric-logo.svg" width="72" height="72" alt="Metric logo">
</p>

# Metric

Metric is a self-hosted observability service for API traffic. It turns request logs into live request rates, error rates, and latency percentiles, and records incidents when traffic crosses configured thresholds. Teams can investigate in a React dashboard backed by a Go API, SQLite, and WebSocket updates.

## Current capabilities

- **Durable ingestion.** Services send authenticated batches with a project API key. Metric commits accepted batches to SQLite before returning HTTP `202` and recovers unexpired records after an API restart.
- **Live metrics and incident history.** The dashboard shows current and historical metrics, endpoint breakdowns, and rule-based incidents. After a WebSocket interruption, it reconnects with fresh authorization and refetches bounded history.
- **Optional Gemini Decisions analysis.** A background worker uses aggregate evidence to add advisory incident context. An evidence gate can withhold the result, and a persistent daily call cap limits provider requests. Rule-based detection and severity remain independent of provider availability.
- **Project access and auditing.** Admins manage projects, owners, viewers, thresholds, and ingest keys. Project reads and live streams enforce access controls. Administrative actions are captured in the audit log.
- **Resource limits and operations.** Ingest admission limits bound records, estimated bytes, endpoint cardinality, active projects, and concurrent requests. SQLite page and free-space controls protect ingestion under disk pressure. An optional operations profile adds Cloudflare R2 backups, restore checks, and Grafana Cloud monitoring.

## Run locally

Use a Linux or WSL shell with Make, Go 1.27.4+, Node.js 22.19+, and Bun 1.4.4+. Python 3 is optional for the traffic simulator.

```bash
make install
ADDR=127.0.0.1:8080 APP_ENV=development AUTH_MODE=dev GEMINI_API_KEY= make dev
```

Open [the local dashboard](http://localhost:5173/dashboard). Vite proxies API and WebSocket requests to the Go service on port 8080. The API reads environment variables from the shell. Development mode grants administrator access without sign-in, so use it only on a trusted local machine with the API bound to loopback.

### Send a request log

Create a project in the dashboard and save the ingest key when it is displayed. Substitute your project ID and key in the request below.

```bash
curl --fail-with-body -X POST 'http://localhost:8080/ingest/<PROJECT_ID>' \
  -H 'Content-Type: application/json' \
  -H 'X-API-Key: <API_KEY>' \
  --data '{"logs":[{"ts":"'"$(date -u +%Y-%m-%dT%H:%M:%SZ)"'","endpoint":"/checkout","method":"POST","status":200,"latency_ms":25,"ip":"203.0.113.7","payload_bytes":512}]}'
```

Send up to 1,000 records and 1 MiB per batch, with timestamps inside the configured live window. HTTP `429` signals an admission limit and includes a `Retry-After` header. HTTP `503` can signal disk pressure or a failed journal write. A retry after an interrupted response can produce duplicate records.

See the [OpenAPI contract](apps/api/openapi.yaml) for request and response schemas.

## Deployment

Copy [`.env.example`](.env.example) to `.env`. Configure the hostname, ACME email, OAuth client credentials, allowed email domains, cookie secret, proxy shared secret, and administrator email list. Register `https://<SITE_ADDRESS>/oauth2/callback` with your OAuth provider.

```bash
docker compose up -d --build
```

Caddy terminates TLS and serves the dashboard. Browser API and WebSocket requests pass through oauth2-proxy. Ingest clients authenticate with the project API key. Only Caddy publishes host ports.

Set `GEMINI_API_KEY` on the server to enable advisory analysis. To enable the optional operations profile, configure R2 and Grafana Cloud and follow the backup and restore procedures in the [operations guide](docs/operations.md).

## Findings and operating limits

- A small, unlabeled historical sample confirmed that the Gemini API accepted the submitted cases. Metric’s evidence gate would have withheld the results. Historical snapshots lacked the anonymous endpoint error summaries needed to support endpoint-specific suggestions, and diagnostic accuracy remains unmeasured. [Incident evaluation](docs/incident-evaluation.md)
- Metric runs as a single process with a single SQLite writer. Admission limits bound configured ingest resources, while overall memory and disk use also depend on copies, indexes, WAL files, backups, and logs. Dashboard recovery covers the retained history returned by the API. [Performance and isolation](docs/performance-hardening.md)

## Development and verification

| Command | Purpose |
| --- | --- |
| `make check` | Run tests, Go vet and lint, and TypeScript checks |
| `make api-contract-check` | Verify generated TypeScript and Zod files against OpenAPI |
| `make web-e2e` | Build the dashboard and run Playwright tests |
| `make perf-smoke` | Run a short isolated k6 exercise |
| `make build` | Build the API binary and dashboard bundle |

See the [architecture guide](docs/architecture.md) for component boundaries and request flows, and the [Makefile](Makefile) for all development targets.
