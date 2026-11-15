#!/usr/bin/env bash
set -euo pipefail

# Each Playwright run owns its temporary database and never uses local secrets.
root="$(cd "$(dirname "$0")/../../.." && pwd)"
tmp="$(mktemp -d)"
pid=""
cleanup() {
  if [[ -n "$pid" ]]; then
    kill -TERM "$pid" 2>/dev/null || true
    wait "$pid" 2>/dev/null || true
  fi
  rm -rf "$tmp"
}
trap cleanup EXIT
trap 'exit 0' INT TERM

(cd "$root/apps/api" && go build -o "$tmp/metric-api" ./app)
unset PORT
ADDR=127.0.0.1:18080 DATABASE_PATH="$tmp/e2e.db" APP_ENV=development \
  AUTH_MODE=dev TRUST_PROXY=false WORKER_INTERVAL=250ms \
  OPENROUTER_API_KEY= GEMINI_API_KEY= "$tmp/metric-api" &
pid=$!
wait "$pid"
