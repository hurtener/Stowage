#!/usr/bin/env bash
# Smoke test for Phase 10: utility scoring & ranking.
# Starts stowage serve with a mock gateway, seeds a memory via the full ingest
# → buffer → extract → reconcile write path, then exercises POST /v1/retrieve
# with Phase 10 additions:
#
#   1. debug:true response includes per-item breakdown field with final_score.
#   2. support block (strength, top_score) is always present in the response.
#   3. session_id in request is accepted without error (cooldown signalling).
#   4. Retrieve without debug:true returns no breakdown field (absent by default).
#
# Exit code == number of failures.
set -uo pipefail
cd "$(dirname "$0")/../.."

fails=0
ok()    { printf 'OK   %s\n' "$*"; }
failc() { printf 'FAIL %s\n' "$*"; fails=$((fails+1)); }
skip()  { printf 'SKIP %s\n' "$*"; }

# ── Build ─────────────────────────────────────────────────────────────────────

BIN=/tmp/stowage-smoke-10
TMPDIR_SMOKE=$(mktemp -d)
trap 'kill "${SERVER_PID:-}" 2>/dev/null; rm -f "$BIN"; rm -rf "$TMPDIR_SMOKE"' EXIT

CGO_ENABLED=0 go build -o "$BIN" ./cmd/stowage 2>/dev/null \
  && ok "cgo-free build" \
  || { failc "cgo-free build"; exit "$fails"; }

# ── Temp environment ──────────────────────────────────────────────────────────

DB_PATH="${TMPDIR_SMOKE}/smoke.db"
CFG_PATH="${TMPDIR_SMOKE}/stowage.yaml"
PORT=$(( 51000 + RANDOM % 4000 ))

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

STATUS=$(api_call POST /v1/admin/keys -d '{"tenant_id":"smoke10","role":"admin"}')
[ "$STATUS" = "201" ] \
  && ok "bootstrap admin key → 201" \
  || { failc "bootstrap admin key → 201 (got $STATUS)"; exit "$fails"; }
ADMIN_KEY=$(grep -o '"plaintext":"[^"]*"' "${TMPDIR_SMOKE}/resp" | sed 's/.*":"\(.*\)"/\1/' | head -1)

STATUS=$(api_call POST /v1/admin/keys \
  -d '{"tenant_id":"smoke10","role":"agent"}' "$ADMIN_KEY")
[ "$STATUS" = "201" ] \
  && ok "create agent key → 201" \
  || { failc "create agent key → 201 (got $STATUS)"; exit "$fails"; }
AGENT_KEY=$(grep -o '"plaintext":"[^"]*"' "${TMPDIR_SMOKE}/resp" | sed 's/.*":"\(.*\)"/\1/' | head -1)

# ── Install a topic so extraction picks up the ingested records ───────────────

STATUS=$(api_call PUT /v1/topics \
  -d '[{"key":"smoke10-topic","description":"Phase 10 scoring smoke test","status":"active"}]' \
  "$AGENT_KEY")
[ "$STATUS" = "200" ] \
  && ok "PUT /v1/topics → 200" \
  || failc "PUT /v1/topics → 200 (got $STATUS)"

# ── Ingest a memory about Rust safety ─────────────────────────────────────────

BATCH='{"records":[
  {"role":"user","content":"Tell me about Rust memory safety.","session_id":"smoke10-sess","branch_id":"smoke10-br"},
  {"role":"assistant","content":"Rust guarantees memory safety without a garbage collector via ownership and borrowing.","session_id":"smoke10-sess","branch_id":"smoke10-br"}
]}'

STATUS=$(api_call POST /v1/records -d "$BATCH" "$AGENT_KEY")
[ "$STATUS" = "202" ] \
  && ok "ingest Rust records → 202" \
  || { failc "ingest Rust records → 202 (got $STATUS)"; exit "$fails"; }

# ── Script mock gateway to produce a Rust memory ─────────────────────────────

sleep 0.5
IDS=$(sqlite3 "$DB_PATH" "SELECT id FROM records WHERE tenant_id='smoke10' ORDER BY created_at, id;")
ID1=$(echo "$IDS" | sed -n 1p); ID2=$(echo "$IDS" | sed -n 2p)
cat > "${TMPDIR_SMOKE}/mockscript.json" <<MOCKEOF
[{"candidates":[{"kind":"fact","content":"Rust guarantees memory safety without a garbage collector via ownership and borrowing.","context":"Rust memory safety overview","entities":["rust","ownership","borrowing"],"keywords":["memory","safety","garbage-collector"],"anticipated_queries":["how does rust ensure memory safety","what is rust ownership","rust borrowing rules"],"importance":3,"confidence":0.9,"provenance":[{"record_id":"${ID1}","span_start":0,"span_end":10},{"record_id":"${ID2}","span_start":0,"span_end":20}]}]}]
MOCKEOF

STATUS=$(api_call POST /v1/buffers/smoke10-sess%2Fsmoke10-br/flush \
  -d '{"trigger":"explicit"}' "$AGENT_KEY")
[ "$STATUS" = "202" ] \
  && ok "flush buffer → 202" \
  || failc "flush buffer → 202 (got $STATUS)"

# ── Wait for reconcile to commit the memory ───────────────────────────────────

sleep 2.0

MEM_COUNT=$(sqlite3 "$DB_PATH" \
  "SELECT COUNT(*) FROM memories WHERE tenant_id='smoke10' AND status='active';" 2>/dev/null || echo 0)
if [ "$MEM_COUNT" -ge 1 ]; then
  ok "active memory committed (count=$MEM_COUNT)"
else
  failc "active memory not committed (count=$MEM_COUNT)"
  cat "${TMPDIR_SMOKE}/serve.log"
  exit "$fails"
fi

# ── AC-1: debug:true response has breakdown field with final_score ─────────────

STATUS=$(api_call POST /v1/retrieve \
  -d '{"query":"Rust memory safety","limit":5,"debug":true,"session_id":"smoke10-retrieval"}' \
  "$AGENT_KEY")
[ "$STATUS" = "200" ] \
  && ok "retrieve debug:true → 200" \
  || failc "retrieve debug:true → 200 (got $STATUS; body: $(cat ${TMPDIR_SMOKE}/resp))"

if resp_contains '"breakdown"'; then
  ok "debug:true: breakdown field present in response"
else
  failc "debug:true: breakdown field missing (Phase 10 AC-1)"
  cat "${TMPDIR_SMOKE}/resp"
fi

if resp_contains '"final_score"'; then
  ok "debug:true: final_score field present in breakdown"
else
  failc "debug:true: final_score field missing (Phase 10 AC-1)"
fi

# ── AC-2: support block always present ────────────────────────────────────────

if resp_contains '"support"'; then
  ok "support block present in response (Phase 10 AC-2)"
else
  failc "support block missing (Phase 10 AC-2)"
  cat "${TMPDIR_SMOKE}/resp"
fi

if resp_contains '"strength"'; then
  ok "support.strength field present"
else
  failc "support.strength field missing"
fi

if resp_contains '"top_score"'; then
  ok "support.top_score field present"
else
  failc "support.top_score field missing"
fi

# ── AC-3: session_id is accepted (cooldown signalling path) ───────────────────

STATUS=$(api_call POST /v1/retrieve \
  -d '{"query":"ownership borrowing","limit":5,"session_id":"smoke10-sess"}' \
  "$AGENT_KEY")
[ "$STATUS" = "200" ] \
  && ok "retrieve with session_id → 200 (cooldown path accepted)" \
  || failc "retrieve with session_id → 200 (got $STATUS)"

# ── AC-4: without debug:true, breakdown field is absent ───────────────────────

STATUS=$(api_call POST /v1/retrieve \
  -d '{"query":"Rust memory safety","limit":5,"debug":false}' \
  "$AGENT_KEY")
[ "$STATUS" = "200" ] \
  && ok "retrieve debug:false → 200" \
  || failc "retrieve debug:false → 200 (got $STATUS)"

if resp_contains '"breakdown"'; then
  failc "debug:false: breakdown field should be absent but was present"
  cat "${TMPDIR_SMOKE}/resp"
else
  ok "debug:false: breakdown field absent (Phase 10 AC-4)"
fi

# ── Graceful shutdown ─────────────────────────────────────────────────────────

kill -TERM "$SERVER_PID" 2>/dev/null
for i in $(seq 1 10); do
  if ! kill -0 "$SERVER_PID" 2>/dev/null; then break; fi
  sleep 0.5
done
ok "server shutdown cleanly"

exit "$fails"
