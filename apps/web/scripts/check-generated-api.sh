#!/usr/bin/env bash
set -euo pipefail

# Never generate into the working tree when checking for drift.
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
METRIC_OPENAPI_OUTPUT="$tmp" bun run api:generate
if ! diff -ru src/generated/api "$tmp"; then
  echo 'Generated API is stale. Run: cd apps/web && bun run api:generate' >&2
  exit 1
fi
echo 'Generated API matches the OpenAPI contract.'
