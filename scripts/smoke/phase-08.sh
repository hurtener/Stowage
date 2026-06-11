#!/usr/bin/env bash
# Smoke test for Phase 08: reconciliation + transactional commit.
# Starts stowage serve with a mock gateway on a random local port and a temp
# SQLite store, exercises the full ingest → buffer → extract → reconcile →
# active-memory write path, then verifies exact-dedup on replay.
# Exit code == number of failures.
set -uo pipefail
cd "$(dirname "$0")/../.."

fails=0
ok()    { printf 'OK   %s\n' "$*"; }
failc() { printf 'FAIL %s\n' "$*"; fails=$((fails+1)); }
skip()  { printf 'SKIP %s\n' "$*"; }

# ── Build ─────────────────────────────────────────────────────────────────────

BIN=/tmp/stowage-smoke-08
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

export STOWAGE_MOCK_SCRIPT="${TMPDIR_SMOKE}/mockscript.json"
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
ok "server started"

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

# ── Bootstrap admin + agent keys ──────────────────────────────────────────────

STATUS=$(api_call POST /v1/admin/keys -d '{"tenant_id":"smoke08","role":"admin"}')
[ "$STATUS" = "201" ] \
  && ok "bootstrap admin key → 201" \
  || { failc "bootstrap admin key → 201 (got $STATUS)"; exit "$fails"; }
ADMIN_KEY=$(json_field "plaintext")

STATUS=$(api_call POST /v1/admin/keys \
  -d '{"tenant_id":"smoke08","role":"agent"}' "$ADMIN_KEY")
[ "$STATUS" = "201" ] \
  && ok "create agent key → 201" \
  || { failc "create agent key → 201 (got $STATUS)"; exit "$fails"; }
AGENT_KEY=$(json_field "plaintext")

# ── Install an explicit topic so extraction is not skipped ───────────────────

STATUS=$(api_call PUT /v1/topics \
  -d '[{"key":"smoke08-topic","description":"Phase 08 smoke test memories","status":"active"}]' \
  "$AGENT_KEY")
[ "$STATUS" = "200" ] \
  && ok "PUT /v1/topics explicit topic → 200" \
  || failc "PUT /v1/topics explicit topic → 200 (got $STATUS)"

# ── Ingest a short conversation ───────────────────────────────────────────────

BATCH='{"records":[
  {"role":"user","content":"What does Go use for concurrency?","session_id":"smoke08-sess","branch_id":"smoke08-br"},
  {"role":"assistant","content":"Go uses goroutines and channels for concurrency.","session_id":"smoke08-sess","branch_id":"smoke08-br"}
]}'

STATUS=$(api_call POST /v1/records -d "$BATCH" "$AGENT_KEY")
[ "$STATUS" = "202" ] \
  && ok "ingest 2 records → 202" \
  || failc "ingest 2 records → 202 (got $STATUS)"

# ── Script the mock gateway with real record IDs (lazy file, entry 0) ────────

sleep 0.5
IDS=$(sqlite3 "$DB_PATH" "SELECT id FROM records WHERE tenant_id='smoke08' ORDER BY created_at, id;")
ID1=$(echo "$IDS" | sed -n 1p); ID2=$(echo "$IDS" | sed -n 2p)
cat > "${TMPDIR_SMOKE}/mockscript.json" <<MOCKEOF
[{"candidates":[{"kind":"fact","content":"Go uses goroutines and channels for concurrency.","context":"smoke08 concurrency discussion","entities":["go","goroutines"],"keywords":["concurrency","channels"],"anticipated_queries":["how does go handle concurrency","what are goroutines","go concurrency primitives"],"importance":3,"confidence":0.9,"provenance":[{"record_id":"${ID1}","span_start":0,"span_end":10},{"record_id":"${ID2}","span_start":0,"span_end":20}]}]}]
MOCKEOF
ok "scripted mock gateway with runtime record ids"

# ── Explicit flush ────────────────────────────────────────────────────────────

STATUS=$(api_call POST /v1/buffers/smoke08-sess%2Fsmoke08-br/flush \
  -d '{"trigger":"explicit"}' "$AGENT_KEY")
[ "$STATUS" = "202" ] \
  && ok "POST /v1/buffers/{key}/flush explicit → 202" \
  || failc "POST /v1/buffers/{key}/flush explicit → 202 (got $STATUS)"

# ── Poll SQLite for memory.added or dedup event ───────────────────────────────

sleep 1.5

ADDED_COUNT=$(sqlite3 "$DB_PATH" \
  "SELECT COUNT(*) FROM events WHERE type='memory.added';" 2>/dev/null || echo 0)
EXTRACT_COUNT=$(sqlite3 "$DB_PATH" \
  "SELECT COUNT(*) FROM events WHERE type='extraction.completed';" 2>/dev/null || echo 0)

if [ "$ADDED_COUNT" -ge 1 ] && [ "$EXTRACT_COUNT" -ge 1 ]; then
  ok "extraction.completed + memory.added events — write path end-to-end"
else
  failc "write path incomplete (extraction=$EXTRACT_COUNT, added=$ADDED_COUNT)"
  cat "${TMPDIR_SMOKE}/serve.log"
fi

# ── Assert memory row + junctions in SQLite ───────────────────────────────────

MEM_COUNT=$(sqlite3 "$DB_PATH" \
  "SELECT COUNT(*) FROM memories WHERE tenant_id='smoke08' AND status='active';" 2>/dev/null || echo 0)
if [ "$MEM_COUNT" = "1" ]; then
  ok "exactly one active memory committed (junction-checked next)"
else
  failc "expected 1 active memory, got $MEM_COUNT"
fi
JCT=$(sqlite3 "$DB_PATH" "SELECT (SELECT COUNT(*) FROM memory_entities)+(SELECT COUNT(*) FROM memory_keywords)+(SELECT COUNT(*) FROM memory_queries)+(SELECT COUNT(*) FROM provenance);" 2>/dev/null || echo 0)
if [ "$JCT" -ge 7 ]; then
  ok "junctions + provenance persisted (rows=$JCT)"
else
  failc "junctions/provenance missing (rows=$JCT)"
fi

# ── Replay the same conversation — assert dedup_exact or no second memory ────

STATUS=$(api_call POST /v1/records -d "$BATCH" "$AGENT_KEY")
[ "$STATUS" = "202" ] \
  && ok "replay ingest 2 records → 202" \
  || failc "replay ingest 2 records → 202 (got $STATUS)"

sleep 0.5
RIDS=$(sqlite3 "$DB_PATH" "SELECT id FROM records WHERE tenant_id='smoke08' ORDER BY created_at, id;")
RID1=$(echo "$RIDS" | sed -n 3p); RID2=$(echo "$RIDS" | sed -n 4p)
cat > "${TMPDIR_SMOKE}/mockscript.json" <<MOCKEOF
[{"used":"entry0 consumed"},{"candidates":[{"kind":"fact","content":"Go uses goroutines and channels for concurrency.","context":"smoke08 concurrency discussion","entities":["go","goroutines"],"keywords":["concurrency","channels"],"anticipated_queries":["how does go handle concurrency","what are goroutines","go concurrency primitives"],"importance":3,"confidence":0.9,"provenance":[{"record_id":"${RID1}","span_start":0,"span_end":10},{"record_id":"${RID2}","span_start":0,"span_end":20}]}]}]
MOCKEOF

STATUS=$(api_call POST /v1/buffers/smoke08-sess%2Fsmoke08-br/flush \
  -d '{"trigger":"explicit"}' "$AGENT_KEY")
[ "$STATUS" = "202" ] \
  && ok "replay flush → 202" \
  || failc "replay flush → 202 (got $STATUS)"

sleep 1.5

DEDUP_COUNT=$(sqlite3 "$DB_PATH" \
  "SELECT COUNT(*) FROM events WHERE type='reconcile.dedup_exact';" 2>/dev/null || echo 0)
MEM_COUNT2=$(sqlite3 "$DB_PATH" \
  "SELECT COUNT(*) FROM memories WHERE tenant_id='smoke08' AND status='active';" 2>/dev/null || echo 0)

if [ "$DEDUP_COUNT" -ge 1 ] && [ "$MEM_COUNT2" = "1" ]; then
  ok "replay → reconcile.dedup_exact (count=$DEDUP_COUNT), still exactly 1 memory"
else
  failc "dedup replay failed (dedup=$DEDUP_COUNT, memories=$MEM_COUNT2; want >=1 and 1)"
  cat "${TMPDIR_SMOKE}/serve.log"
fi

# ── migrate --status shows 0002 ───────────────────────────────────────────────

MIGRATE_OUT=$("$BIN" migrate --config "$CFG_PATH" --status 2>&1 || true)
if echo "$MIGRATE_OUT" | grep -q "0002_content_hash.*applied"; then
  ok "migrate --status lists 0002_content_hash as applied"
else
  failc "migrate --status missing 0002_content_hash applied (output: $MIGRATE_OUT)"
fi

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
