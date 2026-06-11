#!/usr/bin/env bash
# Smoke test for Phase 05: records ingest API, branches, admin keys.
# Starts stowage serve on a random local port with a temp SQLite store,
# exercises all AC-relevant surfaces, then shuts down cleanly via SIGTERM.
# Exit code == number of failures.
set -uo pipefail
cd "$(dirname "$0")/../.."

fails=0
ok()    { printf 'OK   %s\n' "$*"; }
failc() { printf 'FAIL %s\n' "$*"; fails=$((fails+1)); }
skip()  { printf 'SKIP %s\n' "$*"; }

# ── Build ─────────────────────────────────────────────────────────────────────

BIN=/tmp/stowage-smoke-05
TMPDIR_SMOKE=$(mktemp -d)
trap 'rm -f "$BIN"; rm -rf "$TMPDIR_SMOKE"' EXIT

CGO_ENABLED=0 go build -o "$BIN" ./cmd/stowage 2>/dev/null \
  && ok "cgo-free build" \
  || { failc "cgo-free build"; exit "$fails"; }

# ── Temp environment ──────────────────────────────────────────────────────────

DB_PATH="${TMPDIR_SMOKE}/smoke.db"
CFG_PATH="${TMPDIR_SMOKE}/stowage.yaml"

# Pick a random high port.
PORT=$(( 50000 + RANDOM % 10000 ))

cat > "$CFG_PATH" <<YAML
server:
  listen: ":${PORT}"
store:
  driver: sqlite
  dsn: "${DB_PATH}"
gateway:
  driver: mock
YAML

# ── Start server ──────────────────────────────────────────────────────────────

"$BIN" serve --config "$CFG_PATH" >"${TMPDIR_SMOKE}/serve.log" 2>&1 &
SERVER_PID=$!
trap 'kill "$SERVER_PID" 2>/dev/null; rm -f "$BIN"; rm -rf "$TMPDIR_SMOKE"' EXIT

BASE="http://localhost:${PORT}"

# Wait for the server to be ready (up to 10 s).
for i in $(seq 1 20); do
  if curl -sf "${BASE}/healthz" >/dev/null 2>&1; then
    break
  fi
  sleep 0.5
  if [ "$i" -eq 20 ]; then
    failc "server did not start in 10 s"
    cat "${TMPDIR_SMOKE}/serve.log"
    exit "$fails"
  fi
done

# api_call METHOD URL BODY_FLAG BODY_VALUE [AUTH_HEADER]
# Writes response body to $TMPDIR_SMOKE/resp and returns status code.
# Usage: STATUS=$(api_call POST /v1/foo -d '...' [Bearer token])
api_call() {
  local method="$1" url="${BASE}$2" body_flag="${3:-}" body_val="${4:-}" auth="${5:-}"
  local out="${TMPDIR_SMOKE}/resp"
  local args=(-s -X "$method" "$url" -o "$out" -w '%{http_code}')
  [ -n "$auth" ]       && args+=(-H "Authorization: Bearer $auth")
  [ -n "$body_flag" ]  && args+=("$body_flag" "$body_val" -H "Content-Type: application/json")
  curl "${args[@]}" 2>/dev/null
}

# json_field FIELD — extract a top-level string field from last response body.
json_field() {
  grep -o "\"$1\":\"[^\"]*\"" "${TMPDIR_SMOKE}/resp" | sed 's/.*":"\(.*\)"/\1/' | head -1
}

# ── healthz ───────────────────────────────────────────────────────────────────

STATUS=$(api_call GET /healthz)
[ "$STATUS" = "200" ] && ok "GET /healthz → 200" || failc "GET /healthz → 200 (got $STATUS)"

# ── readyz ────────────────────────────────────────────────────────────────────

STATUS=$(api_call GET /readyz)
[ "$STATUS" = "200" ] && ok "GET /readyz → 200" || failc "GET /readyz → 200 (got $STATUS)"

# ── metrics endpoint ──────────────────────────────────────────────────────────

STATUS=$(api_call GET /metrics)
[ "$STATUS" = "200" ] && ok "GET /metrics → 200" || failc "GET /metrics → 200 (got $STATUS)"

# ── Create admin key via bootstrap (keyring empty — no auth required) ─────────

STATUS=$(api_call POST /v1/admin/keys -d '{"tenant_id":"smoke-tenant","role":"admin"}')
if [ "$STATUS" = "201" ]; then
  ok "POST /v1/admin/keys bootstrap → 201"
else
  failc "POST /v1/admin/keys bootstrap → 201 (got $STATUS: $(cat "${TMPDIR_SMOKE}/resp"))"
  kill "$SERVER_PID" 2>/dev/null
  exit "$fails"
fi

ADMIN_KEY=$(json_field "plaintext")
if [ -z "$ADMIN_KEY" ]; then
  failc "bootstrap: no plaintext key in response"
  kill "$SERVER_PID" 2>/dev/null
  exit "$fails"
else
  ok "bootstrap: plaintext key returned"
fi

# ── Second unauthenticated create must fail (keyring no longer empty) ─────────

STATUS=$(api_call POST /v1/admin/keys -d '{"tenant_id":"smoke-tenant","role":"admin"}')
[ "$STATUS" = "401" ] \
  && ok "second unauthenticated create → 401 (bootstrap once only)" \
  || failc "second unauthenticated create → 401 (got $STATUS)"

# ── Create agent key (authenticated) ─────────────────────────────────────────

STATUS=$(api_call POST /v1/admin/keys -d '{"tenant_id":"smoke-tenant","role":"agent"}' "$ADMIN_KEY")
[ "$STATUS" = "201" ] \
  && ok "POST /v1/admin/keys agent → 201" \
  || failc "POST /v1/admin/keys agent → 201 (got $STATUS)"

AGENT_KEY=$(json_field "plaintext")
AGENT_KEY_ID=$(json_field "id")

# ── Ingest single record ──────────────────────────────────────────────────────

STATUS=$(api_call POST /v1/records \
  -d '{"records":[{"role":"user","content":"hello smoke"}]}' "$AGENT_KEY")
[ "$STATUS" = "202" ] && ok "POST /v1/records single → 202" || failc "POST /v1/records single → 202 (got $STATUS)"

# ── Ingest batch ──────────────────────────────────────────────────────────────

BATCH='{"records":[{"role":"user","content":"a"},{"role":"assistant","content":"b"},{"role":"tool","content":"c","outcome":"success"}]}'
STATUS=$(api_call POST /v1/records -d "$BATCH" "$AGENT_KEY")
[ "$STATUS" = "202" ] && ok "POST /v1/records batch → 202" || failc "POST /v1/records batch → 202 (got $STATUS)"

# ── Immutability: no DELETE on /v1/records ────────────────────────────────────

STATUS=$(api_call DELETE /v1/records '' '' "$AGENT_KEY")
[ "$STATUS" = "405" ] && ok "DELETE /v1/records → 405 (immutable)" || failc "DELETE /v1/records → 405 (got $STATUS)"

# ── Cross-tenant forgery rejected ─────────────────────────────────────────────

STATUS=$(api_call POST /v1/records \
  -d '{"records":[{"tenant_id":"evil-tenant","role":"user","content":"forged"}]}' "$AGENT_KEY")
[ "$STATUS" = "403" ] && ok "cross-tenant forgery → 403" || failc "cross-tenant forgery → 403 (got $STATUS)"

# ── Missing auth → 401 ───────────────────────────────────────────────────────

STATUS=$(api_call POST /v1/records \
  -d '{"records":[{"role":"user","content":"no auth"}]}')
[ "$STATUS" = "401" ] && ok "missing auth on ingest → 401" || failc "missing auth on ingest → 401 (got $STATUS)"

# ── Branch fork ───────────────────────────────────────────────────────────────

STATUS=$(api_call POST /v1/branches \
  -d '{"action":"fork","session_id":"sess-smoke"}' "$AGENT_KEY")
[ "$STATUS" = "201" ] && ok "POST /v1/branches fork → 201" || failc "POST /v1/branches fork → 201 (got $STATUS)"
BRANCH_ID=$(json_field "branch_id")

# ── Branch discard ────────────────────────────────────────────────────────────

if [ -n "$BRANCH_ID" ]; then
  STATUS=$(api_call POST /v1/branches \
    -d "{\"action\":\"discard\",\"branch_id\":\"${BRANCH_ID}\"}" "$AGENT_KEY")
  [ "$STATUS" = "200" ] && ok "POST /v1/branches discard → 200" || failc "POST /v1/branches discard → 200 (got $STATUS)"
else
  skip "branch discard (no branch_id)"
fi

# ── Branch fork then merge ────────────────────────────────────────────────────

STATUS=$(api_call POST /v1/branches \
  -d '{"action":"fork","session_id":"sess-smoke-2"}' "$AGENT_KEY")
[ "$STATUS" = "201" ] && ok "POST /v1/branches fork (for merge) → 201" || failc "POST /v1/branches fork (for merge) → 201 (got $STATUS)"
BRANCH_ID2=$(json_field "branch_id")

if [ -n "$BRANCH_ID2" ]; then
  STATUS=$(api_call POST /v1/branches \
    -d "{\"action\":\"merge\",\"branch_id\":\"${BRANCH_ID2}\"}" "$AGENT_KEY")
  [ "$STATUS" = "200" ] && ok "POST /v1/branches merge → 200" || failc "POST /v1/branches merge → 200 (got $STATUS)"
else
  skip "branch merge (no branch_id)"
fi

# ── List keys ─────────────────────────────────────────────────────────────────

STATUS=$(api_call GET /v1/admin/keys '' '' "$ADMIN_KEY")
[ "$STATUS" = "200" ] && ok "GET /v1/admin/keys → 200" || failc "GET /v1/admin/keys → 200 (got $STATUS)"

# ── Revoke agent key ──────────────────────────────────────────────────────────

if [ -n "$AGENT_KEY_ID" ]; then
  STATUS=$(api_call POST "/v1/admin/keys/${AGENT_KEY_ID}/revoke" '' '' "$ADMIN_KEY")
  [ "$STATUS" = "200" ] && ok "POST /v1/admin/keys/{id}/revoke → 200" || failc "POST /v1/admin/keys/{id}/revoke → 200 (got $STATUS)"

  # Revoked key must be rejected immediately — no restart required (AC-6).
  STATUS=$(api_call POST /v1/records \
    -d '{"records":[{"role":"user","content":"after revoke"}]}' "$AGENT_KEY")
  [ "$STATUS" = "401" ] \
    && ok "revoked key rejected immediately → 401 (AC-6)" \
    || failc "revoked key rejected immediately → 401 (got $STATUS)"
else
  skip "revoke agent key (no id extracted)"
fi

# ── DSAR stub → 501 ──────────────────────────────────────────────────────────

STATUS=$(api_call DELETE /v1/admin/users/user-xyz '' '' "$ADMIN_KEY")
[ "$STATUS" = "501" ] && ok "DELETE /v1/admin/users/{user} → 501 stub" || failc "DELETE /v1/admin/users/{user} → 501 (got $STATUS)"

# ── Graceful shutdown via SIGTERM ─────────────────────────────────────────────

kill -TERM "$SERVER_PID" 2>/dev/null

for i in $(seq 1 10); do
  if ! kill -0 "$SERVER_PID" 2>/dev/null; then
    break
  fi
  sleep 0.5
done

if kill -0 "$SERVER_PID" 2>/dev/null; then
  failc "server did not exit after SIGTERM"
  kill -9 "$SERVER_PID" 2>/dev/null
else
  ok "clean SIGTERM shutdown"
fi

exit "$fails"
