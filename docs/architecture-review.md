# Architecture review and hardening

The project remains a Go modular monolith, SQLite and a React SPA on one VPS.
This is appropriate for the stated deployment; no rewrite or new service is
needed. This review is a source-and-test assessment, not a penetration test or
an operational certification.

## High-priority findings addressed

1. **HTTP server crash vulnerability:** Go 1.27.3 was reported by
   `govulncheck` as affected by [GO-2026-6859](https://pkg.go.dev/vuln/GO-2026-6859).
   The Go module and container builder now require 1.27.4. Rebuild old binaries;
   changing source alone does not patch a running process.
2. **Unbounded or misleading live telemetry:** Future timestamps could persist
   for a long time and authenticated clients could fill memory through unlimited
   records or high-cardinality endpoints. Ingest now rejects records older than
   the live window or more than five seconds in the future; small future skew is
   clamped. Record/byte/project/endpoint/concurrency caps reject whole batches
   with 429. Bytes are estimates, not an RSS guarantee. Expired backing arrays
   shrink, and records that expire while awaiting a lock are not acknowledged.
   Snapshot calculations, WebSocket initial writes, metric HTTP responses and
   materialized endpoint history also have concurrency or size limits.
3. **Proxy identity header trust:** Stripping headers on Caddy's auth subrequest
   alone did not explicitly sanitize the original request on every Caddy version.
   Both `/api` and `/ws` now strip caller-supplied identity before the auth check,
   then copy the verified response. `tests/security/caddy-identity.sh` exercises
   missing and verified identities using disposable containers. API proxy mode
   still requires the correct shared secret and the API must stay private.
4. **Empty ACME contact startup failure:** Caddy's required `email` argument
   caused an invalid Caddyfile if the optional `ACME_EMAIL` was empty. Compose
   now supplies the complete directive only when an email is configured.

## Previously addressed in the working tree

- **Deletion and revocation races:** a per-project gate coordinates ingest,
  deletion, rotation/revocation and evaluation. Schema v8 uses project foreign
  keys and cascades; old orphan data is removed during migration. This is a
  single-process fence and is not a distributed protocol.
- **Stream overload:** a full subscriber queue disconnects that subscriber,
  prompting reconnection and bounded history recovery, rather than silently
  continuing after missed messages. History is not unlimited replay.
- **Cross-project contention:** live windows use project-local locks and four
  bounded snapshot workers. Exact percentiles remain exact. A slow project can
  still consume a pool slot; SQLite still permits only one writer at a time.
- **Long-lived authentication:** sockets reconnect after five minutes, but
  oauth2-proxy's separate session refresh policy means identity-provider
  revocation is not guaranteed to be reflected within five minutes.

## Important residual risks and limitations

- **High — recovery and disk exhaustion:** an opt-in R2 backup, restore check and
  Grafana Cloud monitoring stack is available; see `docs/operations.md`. It is
  not protecting a deployment until its bucket, credentials, lifecycle and alert
  routing are configured and verified. A configurable SQLite main-file page cap
  and a low-free-space ingest guard reduce growth risk, but neither is a total
  filesystem quota: WAL, backups, logs and other processes can fill it.
  Snapshot and audit retention reduce growth, but anomaly retention is not
  configured and large endpoint snapshots can grow the database. The 4 MiB history read budget is not a disk
  quota; it can return fewer than 900 points, and an oversized latest row gets
  429. Range reads that exceed the budget fail closed; analysis then has no
  baseline and withholds suggestions.
- **Medium — journal limitations:** unexpired accepted live records now survive
  process restart using FULL-synchronous SQLite WAL. A lost response can still
  cause duplicate ingestion on retry. The journal stores raw IPs and paths;
  pruning is not secure erasure, and backups can retain those bytes. Losing the
  VPS can still lose changes since the latest successful off-site backup.
- **Medium — operational visibility:** `/health` is liveness, not proof that
  ingestion, the database, snapshot processing, analysis jobs or backups are
  healthy. `/ready` checks storage and disk; optional Grafana monitoring adds
  API readiness and analysis queue age to disk, database/WAL and backup checks
  once configured. Snapshot worker lag is logged; application memory and
  broader end-to-end ingestion health still need monitoring.
- **Medium — bounded resources still require capacity planning:** the limits
  restrict admitted live telemetry and some expensive reads, not all process
  allocations or authenticated request rates. Copies, JSON/maps, database
  caches, GC, existing oversized rows, slow connections, logs and external
  traffic can consume resources. Tune limits and use edge-level rate limiting.
- **Medium — advisory AI quality and cost:** Gemini receives anonymized
  aggregate evidence, but it is an external service and should be enabled only
  under an appropriate data-processing policy. The heuristics are not validated
  against labeled local incidents. Output cannot change alert severity or take
  action. Dispatch is reserved before the provider call and uncertain results
  are not retried; a persistent daily request limit bounds application calls
  but is not a monetary cap. See `docs/incident-evaluation.md`.
- **Medium — authorization boundaries:** project, threshold, lifecycle and audit
  read usecases now fail closed without an identity and enforce their admin
  operations; the WebSocket handler also rejects missing identity independently
  of middleware. Metrics/anomaly computation and repositories are internal
  primitives, not independently authorized APIs. New adapters must check
  project access and must not expose those primitives directly. Identity context
  is an in-process trust boundary, not a defense against malicious Go code.
- **Lower — maintainability and UI weight:** `ProjectDetail.tsx` is large and
  production builds warn about JavaScript chunks over 500 kB. Splitting the route
  and profiling actual browser performance are useful follow-up work, not a
  prerequisite to preserving the single-VPS design.

The changes are in the working tree. Run all tests and review this document
before deploying. Back up SQLite before schema migrations, set a Gemini API key in
production `.env` if analysis is desired, and rebuild/restart both API and edge.
See `README.md` for settings and `docs/performance-hardening.md` for benchmark
scope and previous measurements.
