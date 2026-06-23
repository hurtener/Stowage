#!/usr/bin/env bash
# Smoke test for Phase 28: topic-pack composition (D-099). Boots stowage serve with a
# mock gateway, and exercises the composition surface over HTTP:
#   - pack:on:<name> enables a curated pack (source=pack:<name>) alongside explicit topics
#   - explicit + pack compose (union, both visible)
#   - pack:off suppresses packs but keeps explicit topics
# Exit code == number of failures.
set -uo pipefail
cd "$(dirname "$0")/../.."

fails=0
ok()    { printf 'OK   %s\n' "$*"; }
failc() { printf 'FAIL %s\n' "$*"; fails=$((fails+1)); }

BIN=/tmp/stowage-smoke-28
TMPDIR_SMOKE=$(mktemp -d)
trap 'kill "${SERVER_PID:-}" 2>/dev/null; rm -f "$BIN"; rm -rf "$TMPDIR_SMOKE"' EXIT

CGO_ENABLED=0 go build -o "$BIN" ./cmd/stowage 2>/dev/null \
  && ok "cgo-free build" \
  || { failc "cgo-free build"; exit "$fails"; }

DB_PATH="${TMPDIR_SMOKE}/smoke.db"
CFG_PATH="${TMPDIR_SMOKE}/stowage.yaml"
PORT=$(( 50000 + RANDOM % 10000 ))
cat > "$CFG_PATH" <<YAML
server:
  listen: ":${PORT}"
store:
  driver: sqlite
  dsn: "${DB_PATH}"
gateway:
  driver: mock
profile: assistant
YAML

"$BIN" serve --config "$CFG_PATH" >"${TMPDIR_SMOKE}/serve.log" 2>&1 &
SERVER_PID=$!
BASE="http://localhost:${PORT}"
for i in $(seq 1 20); do
  curl -sf "${BASE}/healthz" >/dev/null 2>&1 && break
  sleep 0.5
  [ "$i" -eq 20 ] && { failc "server did not start in 10 s"; cat "${TMPDIR_SMOKE}/serve.log"; exit "$fails"; }
done

RESP="${TMPDIR_SMOKE}/resp"
api() {
  local method="$1" path="$2" body="${3:-}" auth="${4:-}"
  local args=(-s -X "$method" "${BASE}${path}" -o "$RESP" -w '%{http_code}')
  [ -n "$auth" ] && args+=(-H "Authorization: Bearer $auth")
  [ -n "$body" ] && args+=(-H 'Content-Type: application/json' -d "$body")
  curl "${args[@]}" 2>/dev/null
}

# keys
api POST /v1/admin/keys '{"tenant_id":"smoke28","role":"admin"}' >/dev/null
ADMIN_KEY=$(grep -o '"plaintext":"[^"]*"' "$RESP" | sed 's/.*":"\(.*\)"/\1/' | head -1)
api POST /v1/admin/keys '{"tenant_id":"smoke28","role":"agent"}' "$ADMIN_KEY" >/dev/null
AGENT_KEY=$(grep -o '"plaintext":"[^"]*"' "$RESP" | sed 's/.*":"\(.*\)"/\1/' | head -1)

# Enable the project pack + add one explicit topic → composition.
S=$(api PUT /v1/topics '[{"key":"pack:on:project","status":"active"},{"key":"billing-flow","description":"how billing charges customers","status":"active"}]' "$AGENT_KEY")
[ "$S" = "200" ] && ok "PUT pack:on:project + explicit topic → 200" || failc "PUT compose → 200 (got $S)"

S=$(api GET /v1/topics '' "$AGENT_KEY")
[ "$S" = "200" ] && ok "GET /v1/topics (composed) → 200" || failc "GET composed → 200 (got $S)"

grep -q '"source":"pack:project"' "$RESP" \
  && ok "composed set includes pack:project entries (source=pack:project)" \
  || failc "expected source=pack:project in composed set"
grep -q '"billing-flow"' "$RESP" \
  && ok "composed set includes the explicit topic (union)" \
  || failc "explicit topic missing from composed set"
grep -q '"key":"project-glossary"' "$RESP" \
  && ok "pack:project entry project-glossary present" \
  || failc "pack:project entries not composed in"

# pack:off suppresses packs but keeps the explicit topic.
S=$(api PUT /v1/topics '[{"key":"pack:off","status":"active"}]' "$AGENT_KEY")
[ "$S" = "200" ] && ok "PUT pack:off → 200" || failc "PUT pack:off → 200 (got $S)"
S=$(api GET /v1/topics '' "$AGENT_KEY")
grep -q '"source":"pack:project"' "$RESP" \
  && failc "pack:off must suppress the project pack" \
  || ok "pack:off suppresses packs"
grep -q '"billing-flow"' "$RESP" \
  && ok "pack:off keeps explicit topics" \
  || failc "pack:off wrongly dropped the explicit topic"

exit "$fails"
