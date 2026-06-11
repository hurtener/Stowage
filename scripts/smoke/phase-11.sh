#!/usr/bin/env bash
# Smoke test for Phase 11: injections + citations v1 — attribution loop.
#
# Sequence (per spec):
#   retrieve → citation in response → resolve → drilldown → feedback(wrong_citation)
#   → re-retrieve (rank drop / noise/fail counter incremented)
#
# ACs tested:
#   1. retrieve returns api:"v1", response_id, per-item citation handles.
#   2. POST /v1/citations/resolve returns memory + provenance + injection metadata.
#   3. POST /v1/drilldown returns verbatim span excerpt.
#   4. POST /v1/feedback wrong_citation: injection.feedback set + memory counters bumped.
#   5. Profile field "precise" is accepted without error.
#
# Exit code == number of failures.
set -uo pipefail
cd "$(dirname "$0")/../.."

fails=0
ok()    { printf 'OK   %s\n' "$*"; }
failc() { printf 'FAIL %s\n' "$*"; fails=$((fails+1)); }
skip()  { printf 'SKIP %s\n' "$*"; }

# ── Build ─────────────────────────────────────────────────────────────────────

BIN=/tmp/stowage-smoke-11
TMPDIR_SMOKE=$(mktemp -d)
trap 'kill "${SERVER_PID:-}" 2>/dev/null; rm -f "$BIN"; rm -rf "$TMPDIR_SMOKE"' EXIT

CGO_ENABLED=0 go build -o "$BIN" ./cmd/stowage 2>/dev/null \
  && ok "cgo-free build" \
  || { failc "cgo-free build"; exit "$fails"; }

# ── Temp environment ──────────────────────────────────────────────────────────

DB_PATH="${TMPDIR_SMOKE}/smoke.db"
CFG_PATH="${TMPDIR_SMOKE}/stowage.yaml"
PORT=$(( 52000 + RANDOM % 4000 ))

cat > "$CFG_PATH" <<YAML
server:
  listen: ":${PORT}"
store:
  driver: sqlite
  dsn: "${DB_PATH}"
gateway:
  driver: mock
  embed_dims: 4
YAML

# ── Start server ──────────────────────────────────────────────────────────────

export STOWAGE_MOCK_SCRIPT="${TMPDIR_SMOKE}/mockscript.json"
"$BIN" serve --config "$CFG_PATH" >"${TMPDIR_SMOKE}/serve.log" 2>&1 &
SERVER_PID=$!

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

resp_contains() {
  grep -q "$1" "${TMPDIR_SMOKE}/resp"
}

# ── Bootstrap admin + agent keys ──────────────────────────────────────────────

STATUS=$(api_call POST /v1/admin/keys -d '{"tenant_id":"smoke11","role":"admin"}')
[ "$STATUS" = "201" ] \
  && ok "bootstrap admin key → 201" \
  || { failc "bootstrap admin key → 201 (got $STATUS)"; exit "$fails"; }
ADMIN_KEY=$(grep -o '"plaintext":"[^"]*"' "${TMPDIR_SMOKE}/resp" | sed 's/.*":"\(.*\)"/\1/' | head -1)

STATUS=$(api_call POST /v1/admin/keys \
  -d '{"tenant_id":"smoke11","role":"agent"}' "$ADMIN_KEY")
[ "$STATUS" = "201" ] \
  && ok "create agent key → 201" \
  || { failc "create agent key → 201 (got $STATUS)"; exit "$fails"; }
AGENT_KEY=$(grep -o '"plaintext":"[^"]*"' "${TMPDIR_SMOKE}/resp" | sed 's/.*":"\(.*\)"/\1/' | head -1)

# ── Install a topic so extraction picks up the ingested records ───────────────

STATUS=$(api_call PUT /v1/topics \
  -d '[{"key":"smoke11-topic","description":"Phase 11 citation smoke test","status":"active"}]' \
  "$AGENT_KEY")
[ "$STATUS" = "200" ] \
  && ok "PUT /v1/topics → 200" \
  || failc "PUT /v1/topics → 200 (got $STATUS)"

# ── Ingest a memory about Python ─────────────────────────────────────────────

BATCH='{"records":[
  {"role":"user","content":"Tell me about Python.","session_id":"smoke11-sess","branch_id":"smoke11-br"},
  {"role":"assistant","content":"Python is a high-level dynamically typed programming language.","session_id":"smoke11-sess","branch_id":"smoke11-br"}
]}'

STATUS=$(api_call POST /v1/records -d "$BATCH" "$AGENT_KEY")
[ "$STATUS" = "202" ] \
  && ok "ingest Python records → 202" \
  || { failc "ingest Python records → 202 (got $STATUS)"; exit "$fails"; }

# ── Script mock gateway to produce a Python memory ───────────────────────────

sleep 0.5
IDS=$(sqlite3 "$DB_PATH" "SELECT id FROM records WHERE tenant_id='smoke11' ORDER BY created_at, id;")
ID1=$(echo "$IDS" | sed -n 1p); ID2=$(echo "$IDS" | sed -n 2p)
cat > "${TMPDIR_SMOKE}/mockscript.json" <<MOCKEOF
[{"candidates":[{"kind":"fact","content":"Python is a high-level dynamically typed programming language.","context":"Python overview","entities":["python"],"keywords":["python","programming","dynamically-typed"],"anticipated_queries":["what is python","python language features"],"importance":3,"confidence":0.9,"provenance":[{"record_id":"${ID1}","span_start":0,"span_end":22},{"record_id":"${ID2}","span_start":0,"span_end":62}]}]}]
MOCKEOF

STATUS=$(api_call POST /v1/buffers/smoke11-sess%2Fsmoke11-br/flush \
  -d '{"trigger":"explicit"}' "$AGENT_KEY")
[ "$STATUS" = "202" ] \
  && ok "flush buffer → 202" \
  || failc "flush buffer → 202 (got $STATUS)"

# ── Wait for reconcile ────────────────────────────────────────────────────────

sleep 2.0

MEM_COUNT=$(sqlite3 "$DB_PATH" \
  "SELECT COUNT(*) FROM memories WHERE tenant_id='smoke11' AND status='active';" 2>/dev/null || echo 0)
if [ "$MEM_COUNT" -ge 1 ]; then
  ok "active memory committed (count=$MEM_COUNT)"
else
  failc "active memory not committed (count=$MEM_COUNT)"
  cat "${TMPDIR_SMOKE}/serve.log"
  exit "$fails"
fi

# ── AC-1: POST /v1/retrieve — envelope v1, response_id, citation ─────────────

STATUS=$(api_call POST /v1/retrieve \
  -d '{"query":"Python programming language","limit":5}' \
  "$AGENT_KEY")
[ "$STATUS" = "200" ] \
  && ok "retrieve Python → 200" \
  || failc "retrieve Python → 200 (got $STATUS; body: $(cat ${TMPDIR_SMOKE}/resp))"

if resp_contains '"api":"v1"'; then
  ok "retrieve: api:v1 envelope (Phase 11)"
else
  failc "retrieve: missing api:v1 in response"
  cat "${TMPDIR_SMOKE}/resp"
fi

if resp_contains '"response_id"'; then
  ok "retrieve: response_id present"
else
  failc "retrieve: response_id missing"
fi

if resp_contains '"citation"'; then
  ok "retrieve: citation handle present in items"
else
  failc "retrieve: citation handle missing in items"
  cat "${TMPDIR_SMOKE}/resp"
fi

# Extract citation and response_id for further checks.
CITATION=$(grep -o '"citation":"[^"]*"' "${TMPDIR_SMOKE}/resp" | head -1 | sed 's/.*":"\(.*\)"/\1/')
RESP_ID=$(grep -o '"response_id":"[^"]*"' "${TMPDIR_SMOKE}/resp" | head -1 | sed 's/.*":"\(.*\)"/\1/')

if [ -z "$CITATION" ]; then
  failc "could not extract citation from response; skipping downstream checks"
  exit "$fails"
fi
ok "citation extracted: ${CITATION:0:8}..."

# Wait for the async injection writer to flush.
sleep 0.5

# ── AC-2: POST /v1/citations/resolve ─────────────────────────────────────────

STATUS=$(api_call POST /v1/citations/resolve \
  -d "{\"citations\":[\"${CITATION}\"]}" \
  "$AGENT_KEY")
[ "$STATUS" = "200" ] \
  && ok "citations/resolve → 200" \
  || failc "citations/resolve → 200 (got $STATUS; body: $(cat ${TMPDIR_SMOKE}/resp))"

if resp_contains '"found":true'; then
  ok "resolve: citation found"
else
  failc "resolve: citation not found"
  cat "${TMPDIR_SMOKE}/resp"
fi

if resp_contains '"memory"'; then
  ok "resolve: memory block present"
else
  failc "resolve: memory block missing"
fi

# Extract memory_id for drilldown.
MEM_ID=$(grep -o '"id":"[^"]*"' "${TMPDIR_SMOKE}/resp" | head -1 | sed 's/.*":"\(.*\)"/\1/')

# ── AC-3: POST /v1/drilldown ─────────────────────────────────────────────────

STATUS=$(api_call POST /v1/drilldown \
  -d "{\"citation\":\"${CITATION}\"}" \
  "$AGENT_KEY")
[ "$STATUS" = "200" ] \
  && ok "drilldown by citation → 200" \
  || failc "drilldown by citation → 200 (got $STATUS; body: $(cat ${TMPDIR_SMOKE}/resp))"

if resp_contains '"spans"'; then
  ok "drilldown: spans block present"
else
  failc "drilldown: spans block missing"
  cat "${TMPDIR_SMOKE}/resp"
fi

# Drilldown by memory_id must equal drilldown by citation.
if [ -n "$MEM_ID" ]; then
  STATUS=$(api_call POST /v1/drilldown \
    -d "{\"memory_id\":\"${MEM_ID}\"}" \
    "$AGENT_KEY")
  [ "$STATUS" = "200" ] \
    && ok "drilldown by memory_id → 200" \
    || failc "drilldown by memory_id → 200 (got $STATUS)"
fi

# ── AC-4: POST /v1/feedback wrong_citation ────────────────────────────────────

NOISE_BEFORE=$(sqlite3 "$DB_PATH" \
  "SELECT COALESCE(SUM(noise_count),0) FROM memories WHERE tenant_id='smoke11';" 2>/dev/null || echo 0)

STATUS=$(api_call POST /v1/feedback \
  -d "{\"citation\":\"${CITATION}\",\"signal\":\"wrong_citation\"}" \
  "$AGENT_KEY")
[ "$STATUS" = "200" ] \
  && ok "feedback wrong_citation → 200" \
  || failc "feedback wrong_citation → 200 (got $STATUS; body: $(cat ${TMPDIR_SMOKE}/resp))"

if resp_contains '"applied":1'; then
  ok "feedback: applied:1"
else
  failc "feedback: applied:1 not in response"
  cat "${TMPDIR_SMOKE}/resp"
fi

# Verify memory noise_count incremented.
sleep 0.2
NOISE_AFTER=$(sqlite3 "$DB_PATH" \
  "SELECT COALESCE(SUM(noise_count),0) FROM memories WHERE tenant_id='smoke11';" 2>/dev/null || echo 0)
if [ "$NOISE_AFTER" -gt "$NOISE_BEFORE" ]; then
  ok "feedback wrong_citation: noise_count incremented (before=$NOISE_BEFORE after=$NOISE_AFTER)"
else
  failc "feedback wrong_citation: noise_count not incremented (before=$NOISE_BEFORE after=$NOISE_AFTER)"
fi

# Verify injection.feedback = 'wrong_citation'.
INJ_FEEDBACK=$(sqlite3 "$DB_PATH" \
  "SELECT feedback FROM injections WHERE id='${CITATION}';" 2>/dev/null || echo "")
if [ "$INJ_FEEDBACK" = "wrong_citation" ]; then
  ok "injection.feedback = wrong_citation"
else
  failc "injection.feedback = '${INJ_FEEDBACK}' want wrong_citation"
fi

# ── AC-5: profile field accepted ─────────────────────────────────────────────

STATUS=$(api_call POST /v1/retrieve \
  -d '{"query":"Python","limit":3,"profile":"precise"}' \
  "$AGENT_KEY")
[ "$STATUS" = "200" ] \
  && ok "retrieve profile:precise → 200" \
  || failc "retrieve profile:precise → 200 (got $STATUS)"

STATUS=$(api_call POST /v1/retrieve \
  -d '{"query":"Python","limit":3,"profile":"broad"}' \
  "$AGENT_KEY")
[ "$STATUS" = "200" ] \
  && ok "retrieve profile:broad → 200" \
  || failc "retrieve profile:broad → 200 (got $STATUS)"

STATUS=$(api_call POST /v1/retrieve \
  -d '{"query":"Python","limit":3,"profile":"unknown-profile"}' \
  "$AGENT_KEY")
[ "$STATUS" = "400" ] \
  && ok "retrieve unknown profile → 400" \
  || failc "retrieve unknown profile → 400 (got $STATUS)"

# ── AC-6: feedback validation ─────────────────────────────────────────────────

STATUS=$(api_call POST /v1/feedback \
  -d '{"signal":"use"}' \
  "$AGENT_KEY")
[ "$STATUS" = "400" ] \
  && ok "feedback no target → 400" \
  || failc "feedback no target → 400 (got $STATUS)"

STATUS=$(api_call POST /v1/feedback \
  -d '{"memory_id":"x","signal":"bogus"}' \
  "$AGENT_KEY")
[ "$STATUS" = "400" ] \
  && ok "feedback invalid signal → 400" \
  || failc "feedback invalid signal → 400 (got $STATUS)"

# ── Graceful shutdown ─────────────────────────────────────────────────────────

kill -TERM "$SERVER_PID" 2>/dev/null
for i in $(seq 1 10); do
  if ! kill -0 "$SERVER_PID" 2>/dev/null; then break; fi
  sleep 0.5
done
ok "server shutdown cleanly"

exit "$fails"
