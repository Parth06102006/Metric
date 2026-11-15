#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
k6_bin="${K6_BIN:-k6}"
profile="${METRIC_K6_PROFILE:-smoke}"
case "$profile" in smoke|load) ;; *) echo 'METRIC_K6_PROFILE must be smoke or load' >&2; exit 2 ;; esac
if ! command -v "$k6_bin" >/dev/null; then
  echo 'k6 not found. Install k6 v2.3.0 or set K6_BIN to its executable.' >&2
  exit 1
fi

tmp="$(mktemp -d "${TMPDIR:-/tmp}/metric-k6.XXXXXXXX")"
pid=""
cleanup() {
  if [[ -n "$pid" ]]; then
    kill -TERM "$pid" 2>/dev/null || true
    wait "$pid" 2>/dev/null || true
  fi
  rm -rf "$tmp"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# Choose an unused loopback port. The random dev identity below also guards
# against accidentally driving another process if the port is claimed first.
port="$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1]); s.close()')"
email="metric-k6-${RANDOM}-$$@localhost"
base="http://127.0.0.1:${port}"
(cd "$root/apps/api" && go build -o "$tmp/metric-api" ./app)

# Start without inherited credentials or PORT overrides. This is always a
# loopback-only development server with a disposable database and no provider.
env -i PATH="$PATH" HOME="$tmp" ADDR="127.0.0.1:${port}" DATABASE_PATH="$tmp/traffic.db" \
  APP_ENV=development AUTH_MODE=dev DEV_USER_EMAIL="$email" WORKER_INTERVAL=1s \
  DEDUPE_WINDOW=24h LOG_LEVEL=warn OPENROUTER_API_KEY= GEMINI_API_KEY= \
  "$tmp/metric-api" >"$tmp/api.log" 2>&1 &
pid=$!
ready=0
for _ in {1..100}; do
  if ! kill -0 "$pid" 2>/dev/null; then break; fi
  if python3 - "$base" "$email" <<'PY' 2>/dev/null
import json, sys, urllib.request
with urllib.request.urlopen(sys.argv[1] + '/api/auth/me', timeout=1) as response:
    assert json.load(response).get('email') == sys.argv[2]
PY
  then
    ready=1
    break
  fi
  sleep 0.1
done
if [[ "$ready" != 1 ]] || ! kill -0 "$pid" 2>/dev/null; then
  echo 'Isolated API failed to start or its identity did not match:' >&2
  python3 - "$tmp/api.log" <<'PY' >&2
import pathlib, sys
print(pathlib.Path(sys.argv[1]).read_text())
PY
  exit 1
fi

args=(run --quiet)
if [[ -n "${METRIC_K6_SUMMARY:-}" ]]; then
  args+=(--summary-export "$METRIC_K6_SUMMARY")
fi
cd "$root"
echo "Running k6 $profile profile against an isolated API and temporary SQLite database."
env -i PATH="$PATH" HOME="$tmp" METRIC_K6_URL="$base" METRIC_K6_EXPECTED_EMAIL="$email" \
  METRIC_K6_PROFILE="$profile" METRIC_K6_P95_MS="${METRIC_K6_P95_MS:-}" \
  METRIC_K6_LOAD_VUS="${METRIC_K6_LOAD_VUS:-}" METRIC_K6_WS_VUS="${METRIC_K6_WS_VUS:-}" \
  METRIC_K6_RAMP_SECONDS="${METRIC_K6_RAMP_SECONDS:-}" METRIC_K6_HOLD_SECONDS="${METRIC_K6_HOLD_SECONDS:-}" \
  METRIC_K6_BATCH_SIZE="${METRIC_K6_BATCH_SIZE:-}" METRIC_K6_SLEEP_MS="${METRIC_K6_SLEEP_MS:-}" \
  "$k6_bin" "${args[@]}" tests/k6/traffic.js
