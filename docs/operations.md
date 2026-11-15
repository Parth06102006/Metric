# R2 backups and Grafana Cloud monitoring

The operations stack is **opt-in**. It does not create a Cloudflare or Grafana
account, a bucket, lifecycle rules, Grafana alerts or notification contacts.
Until the steps below are completed and verified, there is no real off-site
backup or external alert. Cloudflare R2 and Grafana Cloud can incur charges.

## What runs

`docker compose --profile ops up -d --build` starts three private services:

- `metric-ops` makes a SQLite online backup immediately and approximately hourly
  from the start of each successful run; failures retry after five minutes. If the R2
  settings are absent or invalid, disk metrics remain available but backups
  fail visibly. It verifies SQLite integrity and the expected tables, computes a
  SHA-256 digest, uploads to R2 using the AWS SDK for Go, and checks the uploaded
  object's size and digest metadata. The upload supports multipart objects.
  A success means upload and metadata verification succeeded; it does **not**
  mean the remote bytes have been re-downloaded on every run.
- After the first successful upload and then every 30 days, it downloads the
  latest backup to a separate temporary directory and verifies its digest,
  SQLite integrity and schema. If this check fails, it is retried after the
  next successful backup. This verifies the file, but is not a substitute for
  periodically testing the application against a restored database.
- `node-exporter` reports filesystem metrics; its only data-volume mount is
  read-only. `metric-ops` independently measures the filesystem containing
  `/data/traffic.db`, the database and WAL sizes, and backup/restore status.
  Alloy scrapes both and remote-writes the samples to Grafana Cloud. These
  services publish no ports to the host. Docker network members can still see
  the unauthenticated metrics endpoints, so do not attach untrusted containers.

A temporary snapshot requires local disk space roughly equal to the database
plus its WAL, in addition to a 128 MiB reserve. A restore check also requires
space for the downloaded copy plus that reserve. Data can grow during the check,
so this is a preflight estimate, not a guarantee. Temporary copies and SQLite
sidecars are removed on normal completion or failure; an abrupt kill or power
loss can leave `.metric-backup-*` or `.metric-restore-check-*` directories. After
stopping the ops service and confirming no job is running, an operator may
inspect and remove these specific stale temporary directories. Never remove the
database or its active WAL/SHM files as cleanup.

Snapshots are **plaintext on the VPS** while being prepared. R2 encrypts objects
at rest with Cloudflare-managed keys and TLS protects transport; this setup does
not provide separate client-side encryption. Secure the VPS volume and bucket,
and treat backups as sensitive. See [Cloudflare data security](https://developers.cloudflare.com/r2/reference/data-security/).
The SHA-256 metadata detects accidental corruption, not malicious replacement
by someone who can write both the object and its metadata.

## 1. Configure Cloudflare R2

1. In Cloudflare, enable R2, create a **private** bucket, and note the Cloudflare
   account ID and bucket name. Do not enable public access to the bucket.
2. Create an R2 API token restricted to this bucket, with the object read/write
   permissions needed for upload, listing and restore. Save its access key ID
   and secret access key securely; these are R2 S3 credentials, not a general
   Cloudflare API token. R2's read/write permission can also allow deletion, so
   restrict and protect it. The backup job does not delete completed objects;
   it may abort an incomplete multipart upload on failure.
3. Configure an R2 bucket lifecycle rule to expire completed objects under the
   `metric/` prefix after **30 days**, and a rule to abort old incomplete
   multipart uploads. Use a separate prefix for each deployment. Review
   [R2 lifecycle rules](https://developers.cloudflare.com/r2/buckets/object-lifecycles/)
   and pricing before enabling this: hourly backups for 30 days can mean around
   720 full database copies plus requests. The job does not configure or enforce
   the lifecycle rule itself.
4. Add the following to the production `.env`, using your actual values:

   ```dotenv
   R2_ACCOUNT_ID=<32-character-cloudflare-account-id>
   R2_BUCKET=<private-bucket-name>
   R2_ACCESS_KEY_ID=<r2-access-key-id>
   R2_SECRET_ACCESS_KEY=<r2-secret-access-key>
   R2_PREFIX=metric/
   BACKUP_INTERVAL=1h
   BACKUP_TIMEOUT=50m
   ```

   Keep `.env` out of Git, set restrictive file permissions, and never put secrets
   in command history, screenshots, support tickets or alert labels. Environment
   variables are visible to sufficiently privileged Docker/host administrators.
   The access key stays in the ops container, not the API or Alloy containers.
   `BACKUP_TIMEOUT` must be shorter than `BACKUP_INTERVAL`; allow enough time
   for both upload and the periodic restore check.

## 2. Configure Grafana Cloud

1. Create a Grafana Cloud stack and a token permitted to **write metrics**.
   Obtain the Prometheus remote-write URL and its numeric username from the
   Cloud portal. This is not the Grafana dashboard URL.
2. Put these values in the production `.env`:

   ```dotenv
   GRAFANA_REMOTE_WRITE_URL=https://<prometheus-endpoint>/api/prom/push
   GRAFANA_REMOTE_WRITE_USER=<remote-write-username>
   GRAFANA_REMOTE_WRITE_PASSWORD=<metrics-write-token>
   METRIC_DEPLOYMENT=metric
   ```

   Use a distinct `METRIC_DEPLOYMENT` per VPS so series and alerts do not merge.
   Do not expose the remote-write token in Git. Use HTTPS in production.
3. Start the stack with `docker compose --profile ops up -d --build`. Check
   `docker compose logs metric-ops` for both upload and restore-check success,
   and `docker compose logs alloy` for remote-write errors. Neither startup
   nor a local success log proves the cloud services are configured correctly.
4. In Grafana, import `deploy/monitoring/dashboard.json` and select the Grafana
   Cloud Prometheus data source. Choose the matching deployment name at the top.
   Confirm the graphs show **your database filesystem**, database/WAL sizes,
   backup and exporter health, API readiness, advisory queue depth and daily
   call reservations. `node_filesystem_*{mountpoint="/data"}` is a
   useful secondary check; alert rules use the ops service's direct filesystem
   measurement, not an assumed host mount path.
5. Create alert rules using the PromQL expressions, durations and severities in
   `deploy/monitoring/alerts.yaml`, replacing `metric` with your deployment
   label. These are example Prometheus rule definitions, not automatically
   installed Grafana-managed alerts. Configure and test a Grafana contact point
   and notification policy. Ensure the missing-data alert is evaluated by
   Grafana Cloud, outside the VPS. Test low-disk alerts with a temporary safe
   threshold and test a failed backup, then restore the intended rules.

The default rules warn below 20% available space and escalate below 10%; flag a
failed or older-than-two-hours backup; flag failed/older-than-35-days restore
checks; flag API readiness and a stalled analysis queue; and report missing
monitoring data or unavailable exporters. Allow
extra time for each rule's `for` duration. Node-exporter is a secondary signal;
loss of Alloy or the VPS is caught by the missing-data rule only if Grafana's
external evaluator and alert routing are configured correctly.

## 3. Verify and restore

With R2 configured, run the non-destructive remote check at any time:

```bash
docker compose --profile ops run --rm --no-deps metric-ops verify-latest
```

To download and verify the newest backup into a **new** file, without replacing
production or any existing SQLite sidecars:

```bash
docker compose --profile ops run --rm --no-deps metric-ops restore-latest /data/restored.db
```

If the newest object is corrupt, list the backups and restore a specific older
key instead (choose a key printed by the first command):

```bash
docker compose --profile ops run --rm --no-deps metric-ops list-backups
docker compose --profile ops run --rm --no-deps metric-ops restore-key 'metric/<backup-name>.db' /data/older-restored.db
```

Each download is checksum-, integrity- and schema-checked before installation.
If the destination, its `-wal` or its `-shm` already exists, the command fails.
For a recovery drill, use a separate test volume/instance and confirm
that the API can read expected projects and history; do not test by starting a
second production API against the live database. For real recovery, stop the
API and backup service, preserve the existing database **and** its WAL/SHM
files, restore into an isolated empty location, verify it, then replace the
production database only during downtime and restart the API. Never copy a
live SQLite database or just delete its active WAL. Record and rehearse the
exact procedure for your deployment before an emergency.

## Storage limits and upgrades

Back up the database before upgrading to schema v9. The API defaults to a 2 GiB
SQLite main-file page limit (`SQLITE_MAX_BYTES`) and a 256 MiB filesystem
free-space reserve (`MIN_FREE_DISK_BYTES`). Both accept byte values of at least
1 MiB; size them for your deployment before starting. Startup fails if an
existing database's page count exceeds the configured main-file cap. WAL and
migration/backup workspace require additional filesystem headroom. Pruning can
free pages for reuse without shrinking the file; do not delete a live WAL.

While reserve space or SQLite capacity is exhausted, ingestion returns 503 and
other writes can fail; investigate, free capacity safely or raise a suitable
limit. Raising the limit does not create physical disk space. The check is not an
OS quota and cannot prevent other processes or files from filling the volume.
The ingest journal retains raw IPs and paths, including in backups. Database
files are made private (mode 0600), but secure the whole volume, backup bucket,
case exports and any pre-existing copies. Deletion is not secure erasure.

Do not lower live-memory limits below the unexpired records accepted before a
restart: recovery fails closed rather than silently dropping those records. A
lost HTTP response can still cause duplicate records when a client retries.

## Limits and remaining work

- SQLite includes a live-record journal containing raw IPs and endpoint paths.
  Unexpired committed records recover after process restart, but backups can
  retain their bytes for the bucket's 30-day lifecycle (and deletion from
  SQLite does not securely erase free pages or WAL). Backups may miss changes
  since the last *successful* upload if the VPS is lost; failures extend the gap.
- Alerts require the external Grafana configuration and a working notification
  destination. Verify both. The persisted success timestamp is local bookkeeping,
  not proof that an object has not since been deleted from R2.
- The API's `SQLITE_MAX_BYTES` limits SQLite main-file pages, not WAL, backups,
  logs, Alloy's remote-write WAL or other filesystem usage. It rejects ingest
  near `MIN_FREE_DISK_BYTES`, but external writes can still fill the filesystem.
  At the page limit, other database writes may also fail; pruning does not
  necessarily shrink the main file. Incident retention is not automated.
- `/health` is liveness. Internal `/ready` checks database connectivity, the
  filesystem reserve and page-budget pressure, not every worker or third-party
  service. Analysis queue age and probe failure alerts are separate signals.
- Bucket lifecycle, credentials and alert rules are manual configuration.
  Rotating/revoking secrets and periodically testing restores are operator duties.

For disposable local validation, Go tests exercise SQLite WAL backups, signed
S3-compatible requests, multipart transfer, corrupt downloads and a restored
application database. `bash tests/monitoring/docker-smoke.sh` tests Alloy
scraping and forwarding to a fake local endpoint; no real R2/Grafana account is
involved. The example alert expressions are tested with Prometheus `promtool`.
