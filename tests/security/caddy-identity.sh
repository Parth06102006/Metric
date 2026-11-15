#!/usr/bin/env bash
set -euo pipefail

# Optional Docker regression: fake auth emits either a verified identity or none.
# Only synthetic values are used; containers/network are unique and removed.
root="$(cd "$(dirname "$0")/../.." && pwd)"
tmp="$(mktemp -d)"
prefix="metric-caddy-test-$$"
network="$prefix-net"; fake="$prefix-fake"; edge="$prefix-edge"
cleanup() {
  docker rm -f "$edge" "$fake" >/dev/null 2>&1 || true
  docker network rm "$network" >/dev/null 2>&1 || true
  rm -rf "$tmp"
}
trap cleanup EXIT
cat >"$tmp/fake.Caddyfile" <<'EOF'
:4180 {
  handle /oauth2/auth {
    @verified header X-Test-Verified yes
    handle @verified {
      header X-Auth-Request-Email verified@example.com
      respond "ok" 200
    }
    respond "ok" 200
  }
}
:8080 {
  respond "{header.X-Auth-Request-Email}|{header.X-Proxy-Secret}" 200
}
EOF
docker network create "$network" >/dev/null
docker run -d --name "$fake" --network "$network" --network-alias api --network-alias oauth2-proxy \
  -v "$tmp/fake.Caddyfile:/etc/caddy/Caddyfile:ro" caddy:2-alpine >/dev/null
# Docker chooses a free localhost port; this never replaces an existing service.
docker run -d --name "$edge" --network "$network" -p 127.0.0.1::80 \
  -e SITE_ADDRESS=:80 -e CADDY_ACME_OPTION= -e PROXY_SHARED_SECRET=test-only \
  -v "$root/deploy/Caddyfile:/etc/caddy/Caddyfile:ro" caddy:2-alpine >/dev/null
port="$(docker port "$edge" 80/tcp | awk -F: 'NR==1 {print $NF}')"
for i in {1..30}; do
  if curl -fsS -o /dev/null "http://127.0.0.1:$port/api/probe" 2>/dev/null; then break; fi
  sleep .2
done
for path in api/probe ws/probe; do
  actual="$(curl -fsS -H 'X-Auth-Request-Email: attacker@example.com' -H 'X-Proxy-Secret: attacker' "http://127.0.0.1:$port/$path")"
  verified="$(curl -fsS -H 'X-Auth-Request-Email: attacker@example.com' -H 'X-Test-Verified: yes' "http://127.0.0.1:$port/$path")"
  [[ "$actual" == '|test-only' && "$verified" == 'verified@example.com|test-only' ]] || { echo "identity-header regression: $path" >&2; exit 1; }
  echo "$path: spoofed identity removed; verified identity forwarded"
done
