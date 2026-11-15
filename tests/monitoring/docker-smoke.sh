#!/usr/bin/env bash
# Disposable isolated network; no cloud credentials or project DB are used.
set -euo pipefail
root=$(cd "$(dirname "$0")/../.." && pwd)
tmp=$(mktemp -d)
name="metric-monitor-test-$$-$RANDOM"
cleanup() {
  docker rm -f "${name}-alloy" "${name}-node" "${name}-fake" >/dev/null 2>&1 || true
  docker network rm "$name" >/dev/null 2>&1 || true
  docker volume rm "$name" "${name}-alloy" >/dev/null 2>&1 || true
  rm -rf "$tmp"
}
trap cleanup EXIT
CGO_ENABLED=0 go build -o "$tmp/fake" "$root/tests/monitoring/fake-receiver.go"
chmod 755 "$tmp" "$tmp/fake"
docker network create "$name" >/dev/null
docker volume create "$name" >/dev/null
docker volume create "${name}-alloy" >/dev/null
docker run -d --name "${name}-fake" --network "$name" --network-alias metric-ops --network-alias receiver -v "$tmp/fake:/fake:ro" alpine:3.22 /fake >/dev/null
docker run -d --name "${name}-node" --network "$name" --network-alias node-exporter -v "$name:/data:ro" prom/node-exporter:v1.9.1 --collector.disable-defaults --collector.filesystem >/dev/null
docker run -d --name "${name}-alloy" --network "$name" -e GRAFANA_REMOTE_WRITE_URL=http://receiver:9201/write -e GRAFANA_REMOTE_WRITE_USER=test -e GRAFANA_REMOTE_WRITE_PASSWORD=test -e METRIC_DEPLOYMENT=smoke -v "$root/deploy/monitoring/config.alloy:/etc/alloy/config.alloy:ro" -v "${name}-alloy:/var/lib/alloy" grafana/alloy:v1.11.3 run --server.http.listen-addr=127.0.0.1:12345 --storage.path=/var/lib/alloy /etc/alloy/config.alloy >/dev/null
sleep 40
counts=$(docker exec "${name}-fake" wget -qO- http://127.0.0.1:9201/counts)
read -r writes scrapes <<<"$counts"
if (( writes < 1 || scrapes < 1 )); then
  echo "Alloy failed to scrape or forward: writes=$writes scrapes=$scrapes" >&2
  docker logs "${name}-alloy" >&2
  exit 1
fi
docker exec "${name}-fake" sh -c 'wget -qO- http://node-exporter:9100/metrics | grep -q "node_filesystem_size_bytes.*mountpoint=\"/data\""'
echo "Alloy scraped the private exporter and forwarded metrics; node exporter observed /data."
