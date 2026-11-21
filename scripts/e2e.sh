#!/usr/bin/env bash
# End-to-end run of the panel: builds the frontend and the bridge (with the
# panel embedded), starts cmd/fakecpa, seeds demo data, starts the bridge and
# runs Playwright against it. Extra arguments go to `playwright test`.
#
# Needs an EMPTY database and Redis database:
#   BRIDGE_DATABASE_URL, BRIDGE_REDIS_URL
# Optional: E2E_PORT (default 18080), FAKECPA_LISTEN (default 127.0.0.1:18500).
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
: "${BRIDGE_DATABASE_URL:?set BRIDGE_DATABASE_URL to an empty database}"
: "${BRIDGE_REDIS_URL:?set BRIDGE_REDIS_URL to an empty Redis database}"
port="${E2E_PORT:-18080}"
fake="${FAKECPA_LISTEN:-127.0.0.1:18500}"
work="$(mktemp -d)"

export BRIDGE_SECRET_KEY="${BRIDGE_SECRET_KEY:-MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=}"
export BRIDGE_BOOTSTRAP_ADMIN_USERNAME="${BRIDGE_BOOTSTRAP_ADMIN_USERNAME:-admin}"
export BRIDGE_BOOTSTRAP_ADMIN_PASSWORD="${BRIDGE_BOOTSTRAP_ADMIN_PASSWORD:-admin-pass-123}"
export BRIDGE_COOKIE_SECURE=false
export BRIDGE_LOG_LEVEL="${BRIDGE_LOG_LEVEL:-warn}"
export BRIDGE_LISTEN="127.0.0.1:${port}"
export BRIDGE_METRICS_LISTEN="127.0.0.1:$((port + 1))"

pids=()
cleanup() {
  for p in "${pids[@]}"; do kill "$p" 2>/dev/null || true; done
  wait 2>/dev/null || true
}
trap cleanup EXIT

if [ "${E2E_SKIP_BUILD:-}" != "1" ]; then
  (cd "$root/web" && pnpm build)
fi
(cd "$root/backend" && go build -o "$work/bridge" ./cmd/bridge && go build -o "$work/fakecpa" ./cmd/fakecpa)

"$work/fakecpa" -listen "$fake" -delay 50ms >"$work/fakecpa.log" 2>&1 &
pids+=($!)
sleep 1
"$work/bridge" seed-demo -fakecpa "http://$fake"

"$work/bridge" >"$work/bridge.log" 2>&1 &
pids+=($!)
for _ in $(seq 1 50); do
  curl -fsS "http://127.0.0.1:${port}/readyz" >/dev/null 2>&1 && break
  sleep 0.2
done

status=0
(cd "$root/web" && E2E_BASE_URL="http://127.0.0.1:${port}" \
  E2E_ADMIN_USER="$BRIDGE_BOOTSTRAP_ADMIN_USERNAME" E2E_ADMIN_PASSWORD="$BRIDGE_BOOTSTRAP_ADMIN_PASSWORD" \
  pnpm exec playwright test "$@") || status=$?
if [ "$status" -ne 0 ]; then
  echo "---- bridge log (tail) ----"
  tail -n 50 "$work/bridge.log" || true
fi
exit "$status"
