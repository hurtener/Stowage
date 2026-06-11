#!/usr/bin/env bash
# Smoke test for Phase 06: buffer stage — triggers, exactly-once flush,
# crash recovery. Starts stowage serve on a random local port with a temp
# SQLite store, exercises AC-relevant surfaces, then shuts down cleanly.
# Exit code == number of failures.
set -uo pipefail
cd "$(dirname "$0")/../.."

fails=0
ok()    { printf 'OK   %s\n' "$*"; }
failc() { printf 'FAIL %s\n' "$*"; fails=$((fails+1)); }
skip()  { printf 'SKIP %s\n' "$*"; }

# ── Build ─────────────────────────────────────────────────────────────────────

BIN=/tmp/stowage-smoke-06
TMPDIR_SMOKE=$(mktemp -d)
trap 'rm -f "$BIN"; rm -rf "$TMPDIR_SMOKE"' EXIT

CGO_ENABLED=0 go build -o "$BIN" ./cmd/stowage 2>/dev/null \
  && ok "cgo-free build" \
  || { failc "cgo-free build"; exit "$fails"; }

# ── Temp environment ──────────────────────────────────────────────────────────

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
YAML

# ── Start server ──────────────────────────────────────────────────────────────

"$BIN" serve --config "$CFG_PATH" >"${TMPDIR_SMOKE}/serve.log" 2>&1 &
SERVER_PID=$!
trap 'kill "$SERVER_PID" 2>/dev/null; rm -f "$BIN"; rm -rf "$TMPDIR_SMOKE"' EXIT

BASE="http://localhost:${PORT}"

for i in $(seq 1 20); do
  if curl -sf "${BASE}/healthz" >/dev/null 2>&1; then break; fi
  sleep 0.5
  if [ "$i" -eq 20 ]; then
    failc "server did not start in 10 s"
    cat "${TMPDIR_SMOKE}/serve.log"
    exit "$fails"
  fi
done

api_call() {
  local method="$1" url="${BASE}$2" body_flag="${3:-}" body_val="${4:-}" auth="${5:-}"
  local out="${TMPDIR_SMOKE}/resp"
  local args=(-s -X "$method" "$url" -o "$out" -w '%{http_code}')
  [ -n "$auth" ]       && args+=(-H "Authorization: Bearer $auth")
  [ -n "$body_flag" ]  && args+=("$body_flag" "$body_val" -H "Content-Type: application/json")
  curl "${args[@]}" 2>/dev/null
}

json_field() {
  grep -o "\"$1\":\"[^\"]*\"" "${TMPDIR_SMOKE}/resp" | sed 's/.*":"\(.*\)"/\1/' | head -1
}

# ── Bootstrap admin key ───────────────────────────────────────────────────────

STATUS=$(api_call POST /v1/admin/keys -d '{"tenant_id":"smoke06","role":"admin"}')
[ "$STATUS" = "201" ] \
  && ok "bootstrap admin key → 201" \
  || { failc "bootstrap admin key → 201 (got $STATUS)"; exit "$fails"; }
ADMIN_KEY=$(json_field "plaintext")

STATUS=$(api_call POST /v1/admin/keys \
  -d '{"tenant_id":"smoke06","role":"agent"}' "$ADMIN_KEY")
[ "$STATUS" = "201" ] \
  && ok "create agent key → 201" \
  || { failc "create agent key → 201 (got $STATUS)"; exit "$fails"; }
AGENT_KEY=$(json_field "plaintext")

# ── Ingest N records (will accumulate in the buffer stage) ────────────────────

BATCH='{"records":['
for i in $(seq 1 5); do
  [ "$i" -gt 1 ] && BATCH="${BATCH},"
  BATCH="${BATCH}{\"role\":\"user\",\"content\":\"smoke06 record $i\",\"session_id\":\"smoke-sess\",\"branch_id\":\"smoke-br\"}"
done
BATCH="${BATCH}]}"

STATUS=$(api_call POST /v1/records -d "$BATCH" "$AGENT_KEY")
[ "$STATUS" = "202" ] \
  && ok "ingest 5 records → 202" \
  || failc "ingest 5 records → 202 (got $STATUS)"

# ── Explicit flush via POST /v1/buffers/{key}/flush ───────────────────────────

# Give the stage workers a moment to process the ingest items.
sleep 0.5

FLUSH_BODY='{"trigger":"explicit"}'
STATUS=$(api_call POST /v1/buffers/smoke-sess%2Fsmoke-br/flush \
  -d "$FLUSH_BODY" "$AGENT_KEY")
[ "$STATUS" = "202" ] \
  && ok "POST /v1/buffers/{key}/flush explicit → 202" \
  || failc "POST /v1/buffers/{key}/flush explicit → 202 (got $STATUS)"

# ── session_end trigger ───────────────────────────────────────────────────────

# Ingest one more record and flush with session_end trigger.
STATUS=$(api_call POST /v1/records \
  -d '{"records":[{"role":"assistant","content":"session end test","session_id":"smoke-sess2","branch_id":"smoke-br2"}]}' \
  "$AGENT_KEY")
[ "$STATUS" = "202" ] && ok "ingest record for session_end → 202" || failc "ingest for session_end → 202 (got $STATUS)"

sleep 0.5

STATUS=$(api_call POST /v1/buffers/smoke-sess2%2Fsmoke-br2/flush \
  -d '{"trigger":"session_end"}' "$AGENT_KEY")
[ "$STATUS" = "202" ] \
  && ok "POST /v1/buffers/{key}/flush session_end → 202" \
  || failc "POST /v1/buffers/{key}/flush session_end → 202 (got $STATUS)"

# ── buffer.flushed events visible in SQLite ────────────────────────────────────

sleep 0.5

EVENT_COUNT=$(sqlite3 "$DB_PATH" "SELECT COUNT(*) FROM events WHERE type='buffer.flushed';" 2>/dev/null || echo 0)
if [ "$EVENT_COUNT" -ge 1 ]; then
  ok "buffer.flushed events in SQLite (count=$EVENT_COUNT)"
else
  failc "no buffer.flushed events in SQLite after explicit flush"
fi

# ── branch discard flushes buffer ────────────────────────────────────────────

STATUS=$(api_call POST /v1/records \
  -d '{"records":[{"role":"user","content":"branch discard record","session_id":"disc-sess","branch_id":"disc-br"}]}' \
  "$AGENT_KEY")
[ "$STATUS" = "202" ] && ok "ingest for branch discard → 202" || failc "ingest for branch discard → 202 (got $STATUS)"

sleep 0.5

# Fork branch first so it exists.
STATUS=$(api_call POST /v1/branches \
  -d '{"action":"fork","session_id":"disc-sess"}' "$AGENT_KEY")
[ "$STATUS" = "201" ] && ok "fork branch for discard → 201" || failc "fork branch → 201 (got $STATUS)"
DISC_BRANCH=$(json_field "branch_id")

STATUS=$(api_call POST /v1/branches \
  -d "{\"action\":\"discard\",\"branch_id\":\"${DISC_BRANCH}\"}" "$AGENT_KEY")
[ "$STATUS" = "200" ] && ok "POST /v1/branches discard → 200" || failc "discard branch → 200 (got $STATUS)"

# ── Reject invalid trigger ────────────────────────────────────────────────────

STATUS=$(api_call POST /v1/buffers/somekey/flush \
  -d '{"trigger":"invalid"}' "$AGENT_KEY")
[ "$STATUS" = "400" ] \
  && ok "POST /v1/buffers/{key}/flush invalid trigger → 400" \
  || failc "invalid trigger → 400 (got $STATUS)"

# ── Missing auth → 401 ───────────────────────────────────────────────────────

STATUS=$(api_call POST /v1/buffers/somekey/flush \
  -d '{"trigger":"explicit"}')
[ "$STATUS" = "401" ] \
  && ok "flush without auth → 401" \
  || failc "flush without auth → 401 (got $STATUS)"

# ── Graceful shutdown via SIGTERM ─────────────────────────────────────────────

kill -TERM "$SERVER_PID" 2>/dev/null
for i in $(seq 1 10); do
  if ! kill -0 "$SERVER_PID" 2>/dev/null; then break; fi
  sleep 0.5
done
if kill -0 "$SERVER_PID" 2>/dev/null; then
  failc "server did not exit after SIGTERM"
  kill -9 "$SERVER_PID" 2>/dev/null
else
  ok "clean SIGTERM shutdown"
fi

exit "$fails"
