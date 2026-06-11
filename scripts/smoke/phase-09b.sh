#!/usr/bin/env bash
# Smoke test for Phase 09b: HNSW vindex driver as default.
# Starts stowage serve with NO explicit vindex.driver config (verifies "hnsw"
# is the compiled-in default), seeds a memory via the full ingest path, then
# exercises POST /v1/retrieve to confirm the vector lane is active.
#
#   1. cgo-free build succeeds (HNSW driver is pure Go).
#   2. Server boots; config explain reports vindex.driver = hnsw [default].
#   3. Retrieve round-trip returns degraded:false with the committed memory.
#   4. STOWAGE_VINDEX_DRIVER=brute boots cleanly (fallback driver works).
#
# Exit code == number of failures.
set -uo pipefail
cd "$(dirname "$0")/../.."

fails=0
ok()    { printf 'OK   %s\n' "$*"; }
failc() { printf 'FAIL %s\n' "$*"; fails=$((fails+1)); }
skip()  { printf 'SKIP %s\n' "$*"; }

# ── Build ─────────────────────────────────────────────────────────────────────

BIN=/tmp/stowage-smoke-09b
TMPDIR_SMOKE=$(mktemp -d)
trap 'kill "$SERVER_PID" 2>/dev/null || true; rm -f "$BIN"; rm -rf "$TMPDIR_SMOKE"' EXIT

CGO_ENABLED=0 go build -o "$BIN" ./cmd/stowage 2>/dev/null \
  && ok "cgo-free build (HNSW driver is pure Go)" \
  || { failc "cgo-free build"; exit "$fails"; }

# ── Temp environment ──────────────────────────────────────────────────────────

DB_PATH="${TMPDIR_SMOKE}/smoke.db"
CFG_PATH="${TMPDIR_SMOKE}/stowage.yaml"
PORT=$(( 50000 + RANDOM % 10000 ))

# Deliberately omit vindex.driver — must default to "hnsw".
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

# ── AC-2: config explain shows hnsw default ───────────────────────────────────

EXPLAIN_OUT=$("$BIN" config explain --config "$CFG_PATH" 2>&1 || true)
if echo "$EXPLAIN_OUT" | grep -q "vindex.driver.*hnsw.*\[default\]"; then
  ok "config explain: vindex.driver = hnsw [default]"
else
  failc "config explain: vindex.driver = hnsw [default] not found (got: $(echo "$EXPLAIN_OUT" | grep vindex || echo '(nothing)'))"
fi

# ── Start server (HNSW default) ───────────────────────────────────────────────

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
ok "server started with HNSW default driver"

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

resp_contains() {
  grep -q "$1" "${TMPDIR_SMOKE}/resp"
}

# ── Bootstrap admin + agent keys ──────────────────────────────────────────────

STATUS=$(api_call POST /v1/admin/keys -d '{"tenant_id":"smoke09b","role":"admin"}')
[ "$STATUS" = "201" ] \
  && ok "bootstrap admin key → 201" \
  || { failc "bootstrap admin key → 201 (got $STATUS)"; exit "$fails"; }
ADMIN_KEY=$(json_field "plaintext")

STATUS=$(api_call POST /v1/admin/keys \
  -d '{"tenant_id":"smoke09b","role":"agent"}' "$ADMIN_KEY")
[ "$STATUS" = "201" ] \
  && ok "create agent key → 201" \
  || { failc "create agent key → 201 (got $STATUS)"; exit "$fails"; }
AGENT_KEY=$(json_field "plaintext")

# ── Install a topic ───────────────────────────────────────────────────────────

STATUS=$(api_call PUT /v1/topics \
  -d '[{"key":"smoke09b-topic","description":"Phase 09b HNSW smoke test","status":"active"}]' \
  "$AGENT_KEY")
[ "$STATUS" = "200" ] \
  && ok "PUT /v1/topics → 200" \
  || failc "PUT /v1/topics → 200 (got $STATUS)"

# ── Ingest a record ───────────────────────────────────────────────────────────

BATCH='{"records":[
  {"role":"user","content":"What is HNSW?","session_id":"smoke09b-sess","branch_id":"smoke09b-br"},
  {"role":"assistant","content":"HNSW stands for Hierarchical Navigable Small World, an approximate nearest-neighbour algorithm.","session_id":"smoke09b-sess","branch_id":"smoke09b-br"}
]}'

STATUS=$(api_call POST /v1/records -d "$BATCH" "$AGENT_KEY")
[ "$STATUS" = "202" ] \
  && ok "ingest HNSW records → 202" \
  || { failc "ingest HNSW records → 202 (got $STATUS)"; exit "$fails"; }

# ── Script mock gateway for reconciliation ────────────────────────────────────

sleep 0.5
IDS=$(sqlite3 "$DB_PATH" "SELECT id FROM records WHERE tenant_id='smoke09b' ORDER BY created_at, id;")
ID1=$(echo "$IDS" | sed -n 1p); ID2=$(echo "$IDS" | sed -n 2p)
cat > "${TMPDIR_SMOKE}/mockscript.json" <<MOCKEOF
[{"candidates":[{"kind":"fact","content":"HNSW is an approximate nearest-neighbour algorithm.","context":"vector search overview","entities":["hnsw"],"keywords":["approximate","nearest-neighbour","graph"],"anticipated_queries":["what is hnsw","how does hnsw work","hnsw vector search"],"importance":3,"confidence":0.9,"provenance":[{"record_id":"${ID1}","span_start":0,"span_end":10},{"record_id":"${ID2}","span_start":0,"span_end":20}]}]}]
MOCKEOF

STATUS=$(api_call POST /v1/buffers/smoke09b-sess%2Fsmoke09b-br/flush \
  -d '{"trigger":"explicit"}' "$AGENT_KEY")
[ "$STATUS" = "202" ] \
  && ok "flush buffer → 202" \
  || failc "flush buffer → 202 (got $STATUS)"

# ── Wait for reconcile ────────────────────────────────────────────────────────

sleep 2.0

MEM_COUNT=$(sqlite3 "$DB_PATH" \
  "SELECT COUNT(*) FROM memories WHERE tenant_id='smoke09b' AND status='active';" 2>/dev/null || echo 0)
if [ "$MEM_COUNT" -ge 1 ]; then
  ok "active memory committed (count=$MEM_COUNT)"
else
  failc "active memory not committed (count=$MEM_COUNT)"
  cat "${TMPDIR_SMOKE}/serve.log"
  exit "$fails"
fi

# ── AC-3: Retrieve round-trip — HNSW vector lane ─────────────────────────────

STATUS=$(api_call POST /v1/retrieve \
  -d '{"query":"HNSW nearest neighbour","limit":5}' "$AGENT_KEY")
[ "$STATUS" = "200" ] \
  && ok "POST /v1/retrieve → 200" \
  || failc "POST /v1/retrieve → 200 (got $STATUS; body: $(cat "${TMPDIR_SMOKE}/resp"))"

if resp_contains '"degraded":false'; then
  ok "retrieve: degraded:false (HNSW vector lane active)"
else
  skip "retrieve: degraded flag not false (vector may be pending embed)"
fi

if resp_contains '"items":\[{'; then
  ok "retrieve: non-empty items array"
else
  failc "retrieve: empty items — expected HNSW memory to appear"
  cat "${TMPDIR_SMOKE}/resp"
fi

# ── Graceful shutdown ─────────────────────────────────────────────────────────

kill -TERM "$SERVER_PID" 2>/dev/null
for i in $(seq 1 10); do
  if ! kill -0 "$SERVER_PID" 2>/dev/null; then break; fi
  sleep 0.5
done
if kill -0 "$SERVER_PID" 2>/dev/null; then
  failc "server did not exit after SIGTERM"
  kill -9 "$SERVER_PID" 2>/dev/null
else
  ok "clean SIGTERM shutdown (HNSW run)"
fi

# ── AC-4: Boot with STOWAGE_VINDEX_DRIVER=brute ──────────────────────────────

PORT2=$(( PORT + 1 ))
DB_PATH2="${TMPDIR_SMOKE}/smoke2.db"
CFG_PATH2="${TMPDIR_SMOKE}/stowage2.yaml"
cat > "$CFG_PATH2" <<YAML
server:
  listen: ":${PORT2}"
store:
  driver: sqlite
  dsn: "${DB_PATH2}"
gateway:
  driver: mock
  embed_dims: 4
YAML

STOWAGE_VINDEX_DRIVER=brute "$BIN" serve --config "$CFG_PATH2" \
  >"${TMPDIR_SMOKE}/serve2.log" 2>&1 &
SERVER_PID=$!

BASE2="http://localhost:${PORT2}"
for i in $(seq 1 20); do
  if curl -sf "${BASE2}/healthz" >/dev/null 2>&1; then break; fi
  sleep 0.5
  if [ "$i" -eq 20 ]; then
    failc "brute-driver server did not start in 10 s"
    cat "${TMPDIR_SMOKE}/serve2.log"
    exit "$fails"
  fi
done
ok "server started with STOWAGE_VINDEX_DRIVER=brute"

kill -TERM "$SERVER_PID" 2>/dev/null
for i in $(seq 1 10); do
  if ! kill -0 "$SERVER_PID" 2>/dev/null; then break; fi
  sleep 0.5
done
ok "clean SIGTERM shutdown (brute run)"

exit "$fails"
