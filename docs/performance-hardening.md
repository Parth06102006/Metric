# Project isolation and stream recovery hardening

## Guarantees and scope

- Ingest authentication/append, snapshot compute/save/evaluate/publish and project
  deletion share one process-local, per-project operation gate. Waits respect
  request cancellation; idle gate entries are removed. Deletion waits for earlier
  operations, purges state, and subsequent operations recheck the project.
- SQLite schema v8 enforces cascading project foreign keys for snapshots,
  anomalies and analysis jobs. Its transactional rebuild preserves valid IDs,
  AUTOINCREMENT high-water marks, diagnoses, job state and leases, and removes
  pre-existing orphan telemetry. Audit events intentionally remain independent.
  Back up before upgrading and allow disk/WAL space and startup time for copying
  retained tables. Existing legacy-v1 inspection tables remain unchanged.
- Queue overflow unregisters/closes only the affected WebSocket subscriber.
  Reconnect runs authentication again and the existing dashboard refetches
  bounded metric/incident history. This is not exactly-once delivery or unlimited
  replay; long gaps can exceed existing history limits.
- Live-record copies/pruning are protected by project-local locks. The global
  map lock is held only for entry management. Earliest-event tracking avoids
  rescanning a retained window on every append; append-side expiry sweeps run
  at most once per second (or once per window if shorter), while reads prune at
  the exact cutoff. Already-expired input is not retained.
  Active discovery uses atomic latest-event timestamps and opportunistic cleanup.
- The snapshot cycle uses four workers, not one goroutine per project, and cycles
  cannot overlap. A shared per-project gate also prevents duplicate overlapping
  evaluation for the same project. Cycle duration/project count are debug logs;
  duration beyond the configured interval is a warning.

## Local measurements

These are synthetic development-machine results on an i5-12450HX, Linux/WSL,
Go module toolchain, not production capacity claims. Short benchmark runs have
visible scheduling/GC variance. No external services or production database were
used. The before versions are from the working tree before this hardening.

| Workload | Before | After |
| --- | --- | --- |
| Exact current snapshot, 60,000 records, 20 endpoints, 200 IPs | about 11.50 MB/op, 593 allocations | about 8.01 MB/op, 347 allocations |
| Same exact snapshot wall time | 12.6–14.9 ms/op | roughly 10.7–12.6 ms/op |
| Eight-project cycle, 6,000 records/project, no SQL/provider | 10.7–11.2 ms/cycle with one worker | 3.6–3.8 ms/cycle with four workers |

The IP map no longer preallocates for the number of requests when actual IP
cardinality is smaller. Percentiles, endpoint evidence and current-snapshot
freshness remain exact/unchanged.

An earlier fixed-window microbenchmark sent 100 already-expired records against
a 6k/60k retained window; old code rescanned the window on every call (roughly
43–807 microseconds) while the earlier hardening skipped the expired input. A
subsequent admission policy now **rejects** expired batches, including a batch
that expires while waiting for a lock. The renamed benchmark measures rejection,
not live append throughput; valid batches still allocate/copy and expiry still
requires occasional compaction.

Concurrent copy-only benchmarking did **not** show a general throughput gain:
copy allocation/GC can dominate and same-project callers still serialize. With
one reader per project (`-cpu=8`), the new mutex profile showed no application
window-lock wait, while deterministic tests also verify that a held project lock
cannot block another project's append/read or active discovery. More CPU/GC work
can still delay unrelated projects on the same host.

## Reproduce

Run benchmarks sequentially on an otherwise idle machine:

```sh
cd apps/api
go test ./metrics/repository/memory -run '^$' -bench BenchmarkLiveWindow -benchmem -count=3
go test ./anomaly -run '^$' -bench BenchmarkSnapshotCycle -benchmem -count=3
go test ./metrics/repository/memory -run '^$' -bench BenchmarkProjectContention -cpu=8 \
  -mutexprofile=/tmp/metric-mutex.pprof -memprofile=/tmp/metric-memory.pprof \
  -o /tmp/metric-memory.test
go tool pprof -top /tmp/metric-memory.test /tmp/metric-mutex.pprof
go tool pprof -top -sample_index=alloc_space /tmp/metric-memory.test /tmp/metric-memory.pprof
```

Regression coverage includes paused authentication versus deletion, paused
snapshot computation versus deletion, stale worker IDs, bounded parallelism,
per-project lock cancellation/cleanup, migration/reopen, cascade/late writes,
analysis completion after deletion, concurrent append/discovery/delete, and real
WebSocket disconnection/reconnect on overflow.

## Remaining limits

This is still a single-process design with SQLite's single writer. Four blocked
projects can occupy the whole evaluation pool; exact copies/sorts and very large
windows can still cost substantial CPU/memory. REST and initial WebSocket reads
still compute fresh snapshots. No cache freshness contract or approximate
histograms were added. Subsequent work added admission limits, durable SQLite
ingest and a SQLite main-file page cap; the measurements above predate durable
ingest and do not measure its fsync or write-contention costs. The controls do
not cap total RSS or filesystem usage. The 60,000-record benchmark above opts
into a higher per-project cap than the production default.
Measure production cycle lag, admission rates, memory, SQL waits and disk growth
before choosing further changes.
