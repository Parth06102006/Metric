#!/usr/bin/env bash
# Run with METRIC_TEST_IMAGE pointing at a locally built image; uses disposable data.
set -euo pipefail
: "${METRIC_TEST_IMAGE:?set METRIC_TEST_IMAGE to the built API image}"
name="metric-ops-test-$$-$RANDOM"
cleanup() {
  docker rm -f "${name}-ops" "${name}-api" >/dev/null 2>&1 || true
  docker network rm "$name" >/dev/null 2>&1 || true
  docker volume rm "$name" >/dev/null 2>&1 || true
}
trap cleanup EXIT
docker network create "$name" >/dev/null
docker volume create "$name" >/dev/null
# Start ops first to check that the shared named volume is writable by the API.
docker run -d --name "${name}-ops" --network "$name" --network-alias ops -v "$name:/data" -e API_READY_URL=http://api:8080/ready --entrypoint /metric-ops "$METRIC_TEST_IMAGE" serve >/dev/null
docker run -d --name "${name}-api" --network "$name" --network-alias api -v "$name:/data" -e DATABASE_PATH=/data/traffic.db -e AUTH_MODE=dev "$METRIC_TEST_IMAGE" >/dev/null
if ! docker run --rm --network "$name" alpine:3.22 sh -ec '
  i=0
  while [ "$i" -lt 30 ]; do
    if wget -qO /tmp/metrics http://ops:9101/metrics && grep -q "metric_database_stat_success 1" /tmp/metrics && grep -q "metric_api_ready 1" /tmp/metrics && grep -q "metric_analysis_queue_stat_success 1" /tmp/metrics; then
      grep -q "metric_backup_last_attempt_success 0" /tmp/metrics
      grep -q "metric_disk_stat_success 1" /tmp/metrics
      exit 0
    fi
    i=$((i+1)); sleep 1
  done
  exit 1
'; then
  docker logs "${name}-api" >&2
  docker logs "${name}-ops" >&2
  exit 1
fi
echo "API and ops share the writable volume; disk metrics remain available without R2 credentials."
