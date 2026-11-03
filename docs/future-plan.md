# Metric future architecture plan

Status: historical recommendation, reviewed against the working tree on 2026-11-01; the "what exists today" section below records that earlier baseline, not the current implementation. Since then, the live-window duplication fix and optional asynchronous Jev queue have been implemented. See `README.md` and `docs/architecture.md` for current behavior. MCP remains deferred. Other phases are proposals, not promises of capacity or availability. The earlier handoff at `/tmp/opencode/metric-handoff.md` contained no product or architecture requirements.

## 1. Executive decision

**Keep Metric as a modular Go monolith with the existing React SPA, Caddy, oauth2-proxy, and SQLite on one VPS.** Make detection reliable, bounded, and observable first. Add AI as a cheap, optional asynchronous enrichment step; add MCP only when an actual agent integration needs it. Upgrade individual components when measured limits or an explicit durability/availability requirement justify them.

This provides the best likely output per dollar for the product as it exists today: one deployable backend, one database, useful real-time detection even without an AI provider, and a clear path to growth without prematurely paying for Kafka, Kubernetes, Redis, multiple services, or a managed database. This is a recommendation, not a benchmark: workload, usage, uptime needs, budget, and customer count have not been provided.

### Comparison

| Option | Output | Cost and complexity | Decision |
| --- | --- | --- | --- |
| Harden the existing single-node modular monolith | Live metrics, reliable alerts and advisory explanations within defined limits | Lowest infrastructure and operational cost; node remains a single point of failure | **Do this first** |
| Single-node monolith plus PostgreSQL and/or a durable local queue | Better write concurrency or accepted-data recovery, depending on which component is added | More software, backups and migration work; still not high availability | Add only for a measured bottleneck or stronger acceptance contract |
| Multiple API/worker replicas with shared storage and messaging | Potentially higher availability and throughput | Highest operating cost; needs partitioning, distributed coordination, fan-out and operational expertise | Defer until justified by measured demand or an explicit HA requirement |

## 2. What exists today

- `compose.yaml`, `deploy/Caddyfile` and `apps/api/app/main.go`: one Go process, a React static app at the edge, SSO via oauth2-proxy, and SQLite in a Docker volume. API and oauth2-proxy ports are not published externally.
- `apps/api/ingest/usecase.go`, `apps/api/internal/rest/ingest_handler.go` and `apps/api/metrics/repository/memory/repository.go`: authenticated, validated batches return HTTP 202 after appending to an in-memory rolling window. **Accepted does not mean durably stored**: a process restart loses the window. The current record limit is per batch, not a total per-project memory cap.
- `apps/api/domain/metric.go`, `apps/api/anomaly/worker.go` and `apps/api/anomaly/usecase.go`: the worker computes exact percentiles from live records on a default five-second cadence, then stores snapshots and evaluates deterministic thresholds. It processes active projects sequentially; the synchronous explanation call can delay detection and other projects.
- `apps/api/internal/llm/client.go`: Gemini is optional and has a deterministic fallback and a per-project time limit between requests. Jev is not implemented; `docs/jev-ai-architecture.md` is a proposal. MCP and agent bearer authorization are also proposals in `docs/mcp-server-architecture.md`.
- `apps/api/metrics/usecase.go` and `apps/api/internal/rest/metrics_handler.go`: historical snapshot queries exist internally, but the current metrics HTTP handler exposes the current snapshot, not a history endpoint. WebSocket broadcasts are best-effort; slow clients can miss events (`apps/api/internal/ws/hub.go`).
- `apps/api/internal/sqlite/db.go`: SQLite uses WAL and a single database connection. Audit writes are best-effort and failure is logged; audit pruning exists (`apps/api/audit/usecase.go`, `apps/api/audit/pruner.go`).
- **Immediate correctness defect:** `ActiveProjects` compacts the live slice in place without saving its shortened length (`apps/api/metrics/repository/memory/repository.go`). A local reproduction using one expired and one live record returned the live record twice. Correct and regression-test this before using the metrics or anomaly output for capacity or quality decisions.

## 3. Design principles

1. **Correctness before intelligence.** Deterministic measurement and threshold detection are authoritative. AI is optional, advisory, and never blocks detection or executes actions.
2. **Be explicit about data loss.** Document what a 202, a persisted incident, and a backup actually guarantee. Choose a stronger ingest contract only when users need and fund it.
3. **Bound work and cost.** Limit batches, per-project rates, memory/cardinality, concurrent AI calls, retries, retention and spend. Overload should fail visibly rather than silently degrade.
4. **Security and privacy by default.** Isolate authentication mechanisms; authorize each project and action; minimize retained and exported data; maintain a verifiable audit trail appropriate to the selected guarantee.
5. **Modularity without distributed systems by default.** Preserve the domain/port/adapter boundaries, but introduce a separate process only when its independent scaling, isolation, or integration benefit exceeds its cost.
6. **Measure outputs, not architecture fashion.** Select changes using alert quality, coverage, recovery, latency and total ownership cost. Set targets below as proposals, not claims about the present system.

## 4. Target architecture: first practical release

```text
Instrumented clients                         Browser
      | batched requests + project key          | HTTPS + SSO
      v                                         v
+-----------------------------------------------------------------+
| Caddy: TLS, static React SPA, edge routing, protected API / WS  |
+----------------------+---------------------------+--------------+
                       v                           v
            +------------------------------------------------+
            | Go modular monolith                            |
            |                                                |
            | ingest -> validate/limit -> bounded live window|
            |                         -> deterministic rules |
            |                         -> persist incident    |
            |                         -> publish notification|
            |                                |               |
            |                     bounded analysis jobs      |
            |                                v               |
            |                   optional Jev/Gemini adapter   |--> provider
            |                                                |
            | authorized REST/history + best-effort WS       |
            +------------------------+-----------------------+
                                     v
                       SQLite WAL + verified off-host backups
```

The diagram is a proposed target: limits, analysis jobs, and verified off-host backup are not implied to exist today. No public MCP port is necessary for this stage.

### 4.1 Ingest and live metrics

- Keep batch ingest and project-key authentication. Specify timestamp skew, late-event policy and whether a batch is all-or-nothing; reject unreasonable timestamps and invalid records consistently. Normalize routes to route templates; cap endpoint and IP cardinality and avoid retaining query strings or sensitive paths. Add per-project and global rate, batch, memory and concurrency limits with explicit 429 or 503 responses and `Retry-After` where appropriate. Never report an item accepted if it was dropped for capacity.
- Start with existing exact percentile semantics. Measure CPU, lock contention, memory per record and worker duration before replacing the algorithm. If necessary, move to bounded time buckets and mergeable histograms/sketches; specify and test approximation error. Never compute a period P95 by averaging snapshot P95 values. Counts/sums/histograms can be merged; percentiles from separate windows cannot be merged exactly without the underlying distribution.
- Current records are kept for roughly `rate × window` entries, plus string/map/slice and runtime overhead; there is no fixed ceiling today. A 60-second window at 100 RPS holds approximately 6,000 records, and at 1,000 RPS 60,000, before accounting for late/future timestamps, bursts, extra capacity and memory overhead. These are illustrative, not capacity measurements.
- Keep the initial contract **best-effort live telemetry**: 202 means accepted into volatile memory, not crash-safe or exactly-once. Client retries can duplicate data because no idempotency key is implemented. Publish this clearly to SDK users.
- If customers require crash-safe acknowledged ingest, make that a separate explicitly versioned contract: persist a bounded local write-ahead queue before acknowledging, assign batch IDs, define retry/deduplication windows, admission behavior, retention and fsync policy, replay on restart, and test crash at every commit boundary. A single local disk still cannot survive loss of the host; off-host replication is a further requirement. Do not call it exactly-once.

### 4.2 Detection, persistence, and streaming

- Separate the deterministic detection path from provider calls. Persist a newly detected incident with deterministic explanation/status first, publish it, then enqueue optional enrichment. In a SQLite transaction insert the incident and its analysis job together so a crash cannot lose a pending job after the incident commits. Failure to persist must be observable; do not claim a durable incident when the write fails.
- Use a small bounded SQLite-backed job table in the same process, not a new broker initially. Store a stable job ID, idempotency key, status, attempts, lease/expiry and timestamps. After a restart reclaim expired leases; cap attempts, use exponential backoff with jitter and track permanently failed jobs. Set per-project fairness and a global concurrency cap. Make job result updates conditional/idempotent. Provider calls and local DB commits **cannot be atomic together**: a crash after a provider responds but before commit may trigger another billable call. Minimize that window; use provider idempotency only if documented and supported.
- Batch or schedule snapshot writes separately from UI streaming if measurements show excessive write load. Keep retention policies explicit for snapshots, incidents, audit events and analysis jobs. Consider lower-resolution historical rollups, but define their semantics and retention first.
- Treat WebSockets as a low-latency hint, not a durable delivery channel. Reconnect with jitter and refetch an authorized current snapshot and incident history; add a scoped history endpoint/cursor if the product needs gaps filled. Test reconnect, slow clients, duplicate messages and project deletion.

### 4.3 Optional AI: Jev or existing Gemini

- Start with deterministic incident text and a small redacted numeric context. If operator feedback demonstrates a useful improvement, add a provider-neutral analysis port, a thin Go Jev adapter, and typed validated responses. Keep the existing Gemini adapter or retire it only after a comparison and explicit migration decision. Never send raw IPs, credentials, request bodies or sensitive URL paths to a provider; use opt-in and a documented data-processing/retention policy.
- Build a baseline using correctly scoped recent history with minimum sample counts, coverage checks and explicit `insufficient_data`. Compare against deterministic thresholds and a simple baseline, rather than assuming a model is necessary. Use closed labels, evidence references, model/version and status; calibrated confidence requires measurement, not just a provider confidence value. Label output **advisory/unverified**, not `verified` merely because a confidence threshold is crossed. Do not assert root cause, rollback or scale recommendations without supporting evidence and human review.
- Apply per-project and global budgets, maximum context size, token/call accounting, timeouts, bounded retries and a circuit breaker. If the queue or budget is exhausted, preserve the incident and show fallback/pending status. Shadow-test on representative incidents, evaluate operator usefulness and false recommendations, and roll out behind a flag. Verify provider model, availability, billing terms, privacy and API contract at implementation time.
- Reconcile before implementing `docs/jev-ai-architecture.md`: its inline synchronous analysis flow conflicts with the non-blocking design here, and its `verified` confidence label should not be treated as proof. The existing plan's token estimate is not a bill: question text and provider-reported billable usage must be counted too.

### 4.4 Security, privacy and operations

- Retain Caddy + SSO for people, separate project ingest keys for machine producers, internal networking and production shared-secret checks. Test identity-header spoofing, tenant isolation, key rotation/revocation, WS access and session expiry. Currently changing project ownership does not disconnect existing subscribers (`apps/api/project/lifecycle.go`); close or reauthorize their streams on ownership changes and bound/revalidate sessions if revocation must interrupt them. Restrict trusted proxy IPs and protect against forged forwarded IPs; never use arbitrary client-provided headers as authoritative identity.
- Security-sensitive writes need a clear audit policy. Current audit logging is best-effort; if tamper-resistant or complete audit is required, specify transaction boundaries, fail-open vs fail-closed behavior, restricted access, separate append-only off-host storage and retention. Avoid logging keys, tokens or personal data; review all detail fields, not just their names.
- Add off-host encrypted backups using SQLite's online backup API or another WAL-safe consistent snapshot; do not simply copy an actively written `.db` file. Set backup cadence, retention, encryption key ownership and access controls; alert on failed/stale backup. Regularly restore into an isolated environment, run integrity checks, and measure recovery. A backup only protects persisted data, not the in-memory window.
- Monitor ingest acceptance/rejection, memory and queue limits, worker cycle duration, snapshot age, SQLite errors/size/WAL, disk free space, missing audits, provider failures/spend, backup age and restore tests. Put the alerting receiver outside the single Metric host, otherwise a dead host cannot report its own failure.

## 5. When to add MCP or split services

**MCP: defer until there is a specific agent consumer and use case.** Start with read-only tools for authorized current metrics and incident history; use a thin protocol adapter around the same domain/usecases, and expose no write or analysis trigger until needed. Keep it private/loopback unless deliberately deployed behind authenticated TLS. Use separate revocable, least-privilege agent credentials, per-project authorization on every API call, short session/revocation policy, audit and resource quotas. Verify the MCP SDK's actual transport and security APIs before implementation. The existing Caddy `/api/*` route requires oauth2-proxy SSO, so simply forwarding a bearer token to that route will not work without an explicit, separately protected routing/auth design. Defend against prompt injection in tool data and never let tool output authorize actions.

The older Jev and MCP proposals refer to schema migration v3, but the current schema version is 5 (`apps/api/internal/sqlite/db.go`); rebase and test upgrades against the actual schema. The MCP draft also combines a permanent unique request key with a time-limited reuse window, and describes timeout nesting inconsistently. Define rerun/deduplication semantics and enforce effective child deadlines and cancellation before implementing either feature.

**PostgreSQL: consider** when measured SQLite contention or write latency exceeds the agreed service target after reasonable batching/indexing and workload tuning, or when multiple durable writers are actually needed. Evaluate a managed instance against operational labor and backups. PostgreSQL alone does not solve volatile live windows, duplicate processing, WebSocket fan-out or failover.

**Separate workers, broker, or HA: consider** only when sustained load or a contractual uptime/data-loss target requires them. Define project partition ownership, shared/replayable live-window state, idempotent event processing, cross-replica WebSocket delivery, migrations, leader election and failure recovery before running multiple replicas. A local WAL, a second API process or an in-memory queue alone does not make the system highly available.

## 6. Cost model and examples

Prices below are public examples checked 2026-11-01, not quotes; availability, region, taxes, currency, free-tier eligibility and provider terms can change. They exclude engineering/on-call labor, domains, DNS, email, identity-provider fees, monitoring, networking overages, support, extra disk and vendor minimums. There is no observed production load or verified benchmark in this repository, so these numbers do **not** establish capacity.

| Item | Example input | Estimated monthly cost |
| --- | --- | --- |
| Single VPS | DigitalOcean Basic, 2 GiB, 1 vCPU, 50 GiB storage, 2,000 GiB transfer | $12/month; 4 GiB / 2 vCPU option $24/month. Select based on load testing, not this table. [Pricing](https://www.digitalocean.com/pricing/droplets) |
| Off-host backup | Cloudflare R2 Standard, 10 GB-month average, within its free monthly storage/operation allowances | $0 for the modeled R2 usage if the account is eligible and remaining allowances are unused; above the allowance $0.015/GB-month plus operation charges. [Pricing](https://developers.cloudflare.com/r2/pricing/) |
| Jev, optional | 4,320 calls/month, 1,000 **billable input tokens per call**, $42/billion input tokens | About $0.18144/month before taxes, minimums or other fees; provider reports output tokens free at the cited rate. [Model pricing](https://docs.typesafe.ai/models), [usage field](https://docs.typesafe.ai/api/system-one) |
| Illustrative total | $12 VPS + eligible free-tier backup + the example Jev usage | Approximately $12.18/month for only those modeled line items; not a production budget or an SLA |

AI example assumptions: one active project; at most one call per minute; incidents for 10% of a 30-day month: `30 × 24 × 60 × 0.10 = 4,320` calls. `4,320 × 1,000 × $42 / 1,000,000,000 = $0.18144`. This is an example workload, not the current implementation's guarantee. All question and context tokens must fit into the 1,000 billable tokens; use provider-reported usage, include retries, and multiply by actual projects and calls. Pricing may change. The existing Gemini provider has a different cost model and is not included.

Snapshot write sizing example: 10 continuously active projects at one snapshot every 5 seconds generate `10 × 30 × 24 × 60 × 60 / 5 = 5,184,000` rows in a 30-day month; with the current seven-day retention, about 1,209,600 snapshot rows remain at steady state, before prune timing. At an **assumed** 1 KiB per row including overhead, seven days would be roughly 1.15 GiB, not a measured SQLite size. Endpoint JSON, indexes, WAL, fragmentation, backup copies and other data can change this materially; measure actual bytes and write load. A longer retention increases storage and backup cost.

Useful budget equation: `monthly total = VPS + backup storage/operations + network overage + AI billable tokens × unit price + identity/monitoring + support + engineering/on-call`. Track cost per active project, per million accepted records, and per useful incident reviewed; define the denominator and include labor when comparing architectures.

## 7. Roadmap and acceptance gates

Numbers below are suggested initial targets to negotiate with the owner; they are not measurements or existing commitments. Each phase should have tests and a rollback path before rollout.

### Phase 0 — establish a baseline

- Record expected projects, peak RPS/burst, request size, cardinality, budget, data sensitivity, retention and permitted data loss. Decide whether best-effort 202 is acceptable and what outage/recovery time the owner needs.
- Fix and regression-test the `ActiveProjects` live-record duplication before evaluating output. Include expired/live interleaving, repeated ticks and append/recent paths.
- Run repeatable simulator/load tests against a representative 2 GiB and/or 4 GiB VPS: varying project counts, sustained/burst ingest, 5xx/latency spikes and slow WS clients. Record accepted/rejected counts, p50/p95 ingest latency, memory, CPU, disk growth, worker cycle duration, missed/delayed detections and total cost. Test cold start and restart. `simulator/simulator.py` is a starting generator, not evidence of capacity: it sends at most one synchronous batch per second, capped by the smaller of requested RPS, batch size and 1,000; the default batch size is 100. Add seeded scenarios, report achieved rate, and use a separate load generator for higher or sustained throughput.
- Define metrics for useful output: detection recall on labeled test scenarios, false alerts per project-day, time from threshold breach to persisted incident, and operator-rated helpfulness of explanations. Preserve the dataset and compare every change against it.

### Phase 1 — safe and useful single-node service

- Document volatile acceptance and restart behavior; enforce bounded resource usage, timestamp/cardinality rules, and per-project/global rate limits. Add internal operational metrics and external uptime/backup alerts.
- Persist deterministic incidents before optional analysis; add bounded background jobs, recover leases and ensure provider failure cannot block detections. Expose analysis pending/fallback/failure status.
- Implement and test encrypted off-host backup/restore, WS resynchronization, and security boundaries. Example gates: under the selected test workload, p95 worker cycle < 5 seconds and no silently dropped accepted records while the process remains running; every test-detected incident is persisted or raises a visible persistence failure. Verify with a deliberately unavailable/slow provider, full queue, restart, full disk, and dropped WebSocket.
- Suggested recovery objectives for discussion: persisted database RPO up to 24 hours with daily backups and RTO under 4 hours in a tested restore; volatile in-memory telemetry has no restart RPO guarantee. Actual objectives depend on tested backup frequency and operator availability. If these objectives are unacceptable, increase backup frequency or design off-host replication, then re-estimate cost.

### Phase 2 — measurable AI value

- Implement provider-neutral typed analysis and baseline context, if evaluation justifies it. Ship behind a flag with privacy controls, budgets, fallbacks and durable job tracking.
- Compare deterministic-only, current Gemini and Jev on the same labeled incidents: useful recommendation rate, unsupported recommendation rate, false certainty, operator review time, latency, billable tokens and dollar cost. Agree thresholds with operators before enabling by default. Model outputs must not change rule severity or take action.

### Phase 3 — only demand-driven expansion

- Add history/rollups or a narrow read-only MCP adapter when the actual product workflow needs them. Add durable ingest, PostgreSQL or distributed components only after a documented trigger, architecture decision record, migration/rollback plan, operational runbook and load/failure tests.
- Revisit this plan after measured load exceeds targets for a sustained period, required RPO/RTO changes, a paying use case needs MCP, or cost per useful incident materially worsens. Record the measured trigger, not a guessed RPS threshold.

## 8. Verification checklist for implementers

- Unit and integration tests for limits, data validation, tenant authorization, retention, baseline semantics and rules; regression-test the existing API contract.
- Crash-injection tests for incident/job transaction, job leases, retry/deduplication and any future durable ingest contract. Confirm failures can produce duplicate provider calls without duplicate incident effects.
- Load and soak tests with many projects, high-cardinality endpoints/IPs, out-of-order timestamps, sustained bursts, slow clients and a slow/failed provider. Record hardware and workload with results.
- Restore an off-host backup into a clean environment, verify database integrity and representative projects/thresholds/incidents/audit records, and time recovery. Test storage exhaustion and alert delivery independently of the Metric host.
- Run the repository's relevant checks (`make test`, `make check`, `make build`; `make api-race` when available). `make fmt` modifies Go files, so do not run it merely to validate an unrelated documentation-only change.

## 9. Sources and related documents

- Current implementation: [architecture.md](architecture.md), `README.md`, `compose.yaml`, and source paths cited above. Repository state is authoritative over older proposals.
- Existing proposals: [jev-ai-architecture.md](jev-ai-architecture.md) and [mcp-server-architecture.md](mcp-server-architecture.md). Treat their implementation-specific details as drafts to reconcile with this plan, not implemented functionality.
- [SQLite online backup API](https://www.sqlite.org/backup.html).
- [DigitalOcean Droplet pricing](https://www.digitalocean.com/pricing/droplets), [Cloudflare R2 pricing](https://developers.cloudflare.com/r2/pricing/), [TypeSafe model pricing](https://docs.typesafe.ai/models) and [System One usage](https://docs.typesafe.ai/api/system-one).
