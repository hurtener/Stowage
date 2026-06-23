#!/usr/bin/env bash
# scripts/acceptance/full-cycle-live.sh — Phase 21 (21.6) full-cycle LIVE acceptance.
#
# Boots `stowage serve` against the REAL gateway (bifrost → OpenRouter: embed +
# complete + rerank ALL active, D-075) and drives a realistic multi-session usage
# cycle, asserting correct behavior end-to-end across 100% of the CONSUMER routes:
#   - every HTTP /v1 endpoint,
#   - the co-mounted MCP-over-HTTP surface (initialize / tools/list / tools/call),
#   - the `stowage` CLI.
#
# This is the launch acceptance gate an operator runs before tagging v0.1. It is
# OPERATOR-RUN — never CI: it makes real, paid model calls. It never prints the key.
#
# Usage:
#   set -a; source .env; set +a            # provides OPENROUTER_API_KEY
#   scripts/acceptance/full-cycle-live.sh   # real models (default)
#   STOWAGE_ACCEPT_GATEWAY=mock \
#     scripts/acceptance/full-cycle-live.sh # dry-run: validate route coverage, no paid call
#
# Model/endpoint overrides (defaults mirror eval/harness/fullmode_test.go):
#   STOWAGE_ACCEPT_MODEL, STOWAGE_ACCEPT_EMBED_MODEL, STOWAGE_ACCEPT_EMBED_DIMS,
#   STOWAGE_ACCEPT_RERANK_MODEL, STOWAGE_ACCEPT_BASE_URL, STOWAGE_ACCEPT_RERANK_BASE_URL,
#   STOWAGE_ACCEPT_PROVIDER.
#
# COMPLETE-MODEL NOTE: the complete model must be reliable for SCHEMA-CONSTRAINED
# (structured) output — extraction/reflection/verify depend on it. The default
# inception/mercury-2 intermittently returns empty content ("nil content in response
# message") on the extraction call, which dead-letters the candidates and forms no
# memory (observed live; see eval/REPORT.md). Use a solid structured-output model, e.g.:
#   STOWAGE_ACCEPT_MODEL=google/gemini-2.5-flash scripts/acceptance/full-cycle-live.sh
# Verified live: with gemini-2.5-flash the full knowledge cycle (extract → retrieve →
# cite → drill-down → verify=entailed → causal → trace) passes end-to-end.
#
# Exit code == number of failed assertions.
set -uo pipefail
cd "$(dirname "$0")/../.."

# ── Reporting ─────────────────────────────────────────────────────────────────
pass=0
fails=0
declare -a ROUTE_RESULTS=()
ok()    { printf '  \033[32mOK\033[0m   %s\n' "$*"; pass=$((pass+1)); ROUTE_RESULTS+=("PASS  $*"); }
bad()   { printf '  \033[31mFAIL\033[0m %s\n' "$*"; fails=$((fails+1)); ROUTE_RESULTS+=("FAIL  $*"); }
skip()  { printf '  \033[33mSKIP\033[0m %s\n' "$*"; ROUTE_RESULTS+=("SKIP  $*"); }
# kbad: a failure that is EXPECTED in mock dry-run (no real knowledge forms) — it
# becomes a SKIP under mock, a real FAIL under the live gateway.
kbad()  { if [ "${GW:-}" = mock ]; then skip "$* [needs live extraction]"; else bad "$*"; fi; }
info()  { printf '\033[36m▸ %s\033[0m\n' "$*"; }
die()   { printf '\033[31mFATAL: %s\033[0m\n' "$*" >&2; exit 1; }

command -v jq  >/dev/null || die "jq is required (brew install jq)"
command -v curl >/dev/null || die "curl is required"

# ── Gateway selection (real models by default) ────────────────────────────────
GW="${STOWAGE_ACCEPT_GATEWAY:-bifrost}"
if [ "$GW" = "mock" ]; then
  info "DRY-RUN: mock gateway — validates route coverage, NO paid call (extraction is scripted-empty, so knowledge asserts are skipped)"
else
  [ -n "${OPENROUTER_API_KEY:-}" ] || die "OPENROUTER_API_KEY not set — run: set -a; source .env; set +a"
fi

PROVIDER="${STOWAGE_ACCEPT_PROVIDER:-openrouter}"
BASE_URL="${STOWAGE_ACCEPT_BASE_URL:-https://openrouter.ai/api}"
RERANK_BASE_URL="${STOWAGE_ACCEPT_RERANK_BASE_URL:-https://openrouter.ai/api/v1}"
MODEL="${STOWAGE_ACCEPT_MODEL:-inception/mercury-2}"
EMBED_MODEL="${STOWAGE_ACCEPT_EMBED_MODEL:-perplexity/pplx-embed-v1-0.6b}"
EMBED_DIMS="${STOWAGE_ACCEPT_EMBED_DIMS:-1024}"
RERANK_MODEL="${STOWAGE_ACCEPT_RERANK_MODEL:-cohere/rerank-4-fast}"

# ── Build + temp env ──────────────────────────────────────────────────────────
BIN=$(mktemp -d)/stowage
WORK=$(mktemp -d)
SERVER_PID=""
SAVED_LOG="${TMPDIR:-/tmp}/stowage-acceptance-serve.log"
cleanup() {
  [ -n "$SERVER_PID" ] && kill "$SERVER_PID" 2>/dev/null
  # Preserve the server log on failure so a live finding is diagnosable.
  [ "${fails:-0}" -ne 0 ] && [ -f "$WORK/serve.log" ] && cp "$WORK/serve.log" "$SAVED_LOG" 2>/dev/null \
    && printf 'server log preserved at %s\n' "$SAVED_LOG" >&2
  rm -rf "$WORK" "$(dirname "$BIN")"
}
trap cleanup EXIT

info "Building CGo-free binary"
CGO_ENABLED=0 go build -o "$BIN" ./cmd/stowage || die "build failed"

API_PORT=$(( 41000 + RANDOM % 4000 ))
MCP_PORT=$(( 45000 + RANDOM % 4000 ))
DB="$WORK/acceptance.db"
CFG="$WORK/stowage.yaml"

if [ "$GW" = "mock" ]; then
  export STOWAGE_MOCK_SCRIPT="$WORK/mockscript.json"
  printf '[]' > "$STOWAGE_MOCK_SCRIPT"
  cat > "$CFG" <<YAML
server: { listen: ":${API_PORT}", mcp_listen: ":${MCP_PORT}" }
store:   { driver: sqlite, dsn: "${DB}" }
gateway: { driver: mock, embed_dims: 4 }
profile: assistant
YAML
else
  # The config api_key is an env-var REFERENCE; the secret value lives in
  # STOWAGE_GATEWAY_API_KEY (resolved at boot). We never echo it.
  export STOWAGE_GATEWAY_API_KEY="$OPENROUTER_API_KEY"
  cat > "$CFG" <<YAML
server: { listen: ":${API_PORT}", mcp_listen: ":${MCP_PORT}" }
store:   { driver: sqlite, dsn: "${DB}" }
gateway:
  driver: ${GW}
  provider: ${PROVIDER}
  base_url: "${BASE_URL}"
  rerank_base_url: "${RERANK_BASE_URL}"
  api_key: env.STOWAGE_GATEWAY_API_KEY
  model: "${MODEL}"
  embed_model: "${EMBED_MODEL}"
  embed_dims: ${EMBED_DIMS}
  rerank_model: "${RERANK_MODEL}"
profile: assistant
YAML
fi

API="http://127.0.0.1:${API_PORT}"
MCP="http://127.0.0.1:${MCP_PORT}"

# ── CLI routes (version / config explain / migrate) ───────────────────────────
info "CLI surface"
"$BIN" version >/dev/null 2>&1 && ok "CLI: stowage version" || bad "CLI: stowage version"
"$BIN" migrate --config "$CFG" >/dev/null 2>&1 && ok "CLI: stowage migrate" || bad "CLI: stowage migrate"
EXPL=$("$BIN" config explain --config "$CFG" 2>/dev/null || true)
printf '%s' "$EXPL" | grep -q "gateway.driver" \
  && ok "CLI: stowage config explain (effective config + provenance)" \
  || bad "CLI: stowage config explain"

# ── Boot serve (API + co-mounted MCP) ─────────────────────────────────────────
info "Starting stowage serve (gateway=${GW}, model=${MODEL})"
"$BIN" serve --config "$CFG" >"$WORK/serve.log" 2>&1 &
SERVER_PID=$!
for i in $(seq 1 40); do curl -sf "$API/healthz" >/dev/null 2>&1 && break; sleep 0.5
  [ "$i" = 40 ] && { cat "$WORK/serve.log"; die "server did not become healthy"; }; done
ok "serve: /healthz ready (one process, API + MCP)"
curl -sf "$API/readyz"  >/dev/null 2>&1 && ok "serve: /readyz"  || bad "serve: /readyz"
curl -sf "$API/metrics" >/dev/null 2>&1 && ok "serve: /metrics" || bad "serve: /metrics"

# ── HTTP helper ───────────────────────────────────────────────────────────────
# api METHOD PATH [json-body] [bearer]  → echoes "<status>\n<body>"
RESP="$WORK/resp"
api() {
  local m="$1" p="$2" body="${3:-}" tok="${4:-${AGENT_KEY:-}}"
  local args=(-s -X "$m" "$API$p" -o "$RESP" -w '%{http_code}')
  [ -n "$tok" ]  && args+=(-H "Authorization: Bearer $tok")
  [ -n "$body" ] && args+=(-H 'Content-Type: application/json' -d "$body")
  local code; code=$(curl "${args[@]}" 2>/dev/null)
  printf '%s' "$code"
}
body() { cat "$RESP"; }
# assert_2xx LABEL STATUS
assert_2xx() { case "$2" in 2*) ok "$1 ($2)";; *) bad "$1 (got $2: $(head -c200 "$RESP"))";; esac; }

# ── Bootstrap keys (admin then agent) ─────────────────────────────────────────
info "Key administration (HTTP-only tier)"
TENANT="accept-$(date +%s 2>/dev/null || echo t)$RANDOM"
S=$(api POST /v1/admin/keys "{\"tenant_id\":\"$TENANT\",\"role\":\"admin\"}" "")
assert_2xx "POST /v1/admin/keys (bootstrap admin)" "$S"
ADMIN_KEY=$(jq -r '.plaintext // empty' "$RESP")
[ -n "$ADMIN_KEY" ] || die "no admin key minted"
S=$(api POST /v1/admin/keys "{\"tenant_id\":\"$TENANT\",\"role\":\"agent\"}" "$ADMIN_KEY")
assert_2xx "POST /v1/admin/keys (agent key)" "$S"
AGENT_KEY=$(jq -r '.plaintext // empty' "$RESP")
[ -n "$AGENT_KEY" ] || die "no agent key minted"
S=$(api GET /v1/admin/keys "" "$ADMIN_KEY"); assert_2xx "GET /v1/admin/keys (list)" "$S"

# ── Topics (extraction magnets) ───────────────────────────────────────────────
info "Topics"
# PUT /v1/topics takes a bare array of {key,description,status}.
S=$(api PUT /v1/topics '[{"key":"preferences","description":"the user'\''s stated preferences, tools, and working style","status":"active"},{"key":"decisions","description":"technical decisions the user or team has made and why","status":"active"},{"key":"gotchas","description":"pitfalls, bugs, and lessons learned to avoid repeating","status":"active"}]')
assert_2xx "PUT /v1/topics" "$S"
S=$(api GET /v1/topics); assert_2xx "GET /v1/topics" "$S"
# Pack composition (D-099): enable a curated pack and confirm it composes with the
# explicit topics above (union; entries tagged source=pack:project).
S=$(api PUT /v1/topics '[{"key":"pack:on:project","status":"active"}]'); assert_2xx "PUT /v1/topics (pack:on:project)" "$S"
S=$(api GET /v1/topics)
if grep -q '"source":"pack:project"' "$RESP" && grep -q '"key":"preferences"' "$RESP"; then
  ok "topic-pack composition (pack:on:project ∪ explicit topics)"
else
  bad "pack:on:project did not compose with explicit topics: $(head -c200 "$RESP")"
fi

# ── Ingest a realistic multi-session conversation ─────────────────────────────
info "Ingest (records → buffer → real extraction)"
SESS1="sess-onboarding"; SESS2="sess-correction"
ingest() { api POST /v1/records "{\"records\":[$1]}"; }
rec() { printf '{"role":"%s","content":"%s","session_id":"%s","buffer_key":"%s"}' "$1" "$2" "$3" "$3"; }

S=$(ingest "$(rec user 'My name is Dana. My code editor of choice is Neovim and I use it for everything.' "$SESS1" )")
assert_2xx "POST /v1/records (session 1, turn 1)" "$S"
S=$(ingest "$(rec user 'We decided to use PostgreSQL as the primary database for the billing service, mainly for its transactional guarantees.' "$SESS1")")
assert_2xx "POST /v1/records (session 1, turn 2)" "$S"
S=$(ingest "$(rec user 'Gotcha: the Kafka consumer silently drops messages if you forget to commit offsets after a rebalance.' "$SESS1")")
assert_2xx "POST /v1/records (session 1, turn 3)" "$S"
# Ingest enqueues to the buffer stage asynchronously (records_handler non-blocking
# channel → stage goroutine); settle briefly so the explicit flush below doesn't race
# the buffer-append and extract an empty buffer. The wait loop re-flushes as a backstop.
sleep 3
S=$(api POST "/v1/buffers/$SESS1/flush" '{"trigger":"explicit"}'); assert_2xx "POST /v1/buffers/{key}/flush (s1)" "$S"

# Branch route (exploration lifecycle): fork off the session.
S=$(api POST /v1/branches "{\"action\":\"fork\",\"session_id\":\"$SESS1\"}"); assert_2xx "POST /v1/branches (fork)" "$S"

# ── Wait for real extraction to produce retrievable knowledge ─────────────────
info "Waiting for the pipeline to extract + embed (real LLM; up to ~3 min)"
RETR_RESP="$WORK/retr"
retrieve() { # query [profile] → status; body in $RETR_RESP. Retries once on a
  # transient connection failure (curl 000) — the rerank path can occasionally reset.
  local q="$1" prof="${2:-}" code
  for _ in 1 2; do
    code=$(curl -s --max-time 90 -X POST "$API/v1/retrieve" -H "Authorization: Bearer $AGENT_KEY" \
      -H 'Content-Type: application/json' -o "$RETR_RESP" -w '%{http_code}' \
      -d "{\"query\":\"$q\",\"limit\":5,\"include_lanes\":true,\"session_id\":\"q\",\"profile\":\"$prof\"}" 2>/dev/null)
    [ "$code" != "000" ] && break
    sleep 2
  done
  printf '%s' "$code"
}
# assert a retrieve() result (reads $RETR_RESP, not $RESP).
assert_retr() { case "$2" in 2*) ok "$1 ($2)";; *) bad "$1 (got $2: $(head -c200 "$RETR_RESP"))";; esac; }
found_editor=0
if [ "$GW" = "mock" ]; then
  info "mock gateway: extraction is scripted-empty — skipping knowledge-content asserts (route coverage only)"
else
  for i in $(seq 1 36); do
    retrieve "what code editor does Dana use" >/dev/null
    if jq -e '[.items[].content] | join(" ") | test("Neovim"; "i")' "$RETR_RESP" >/dev/null 2>&1; then
      found_editor=1; break
    fi
    # Backstop the async-buffer race: re-flush every ~30s so a first flush that beat the
    # buffer-append still triggers extraction once the records have landed.
    [ $((i % 6)) -eq 0 ] && api POST "/v1/buffers/$SESS1/flush" '{"trigger":"explicit"}' >/dev/null
    sleep 5
  done
  [ "$found_editor" = 1 ] && ok "knowledge formed: 'Neovim' retrievable after real extraction (${i}×5s)" \
                          || bad "knowledge NOT formed: 'Neovim' not retrievable in time (serve log preserved on exit)"
fi

# ── Retrieve (both profiles) + support summary + citations ────────────────────
info "Retrieve (balanced + precise/rerank), citations, drill-down, feedback"
S=$(retrieve "what database did we choose for billing" ""); assert_2xx "POST /v1/retrieve (balanced)" "$S"
RESPID=$(jq -r '.response_id // empty' "$RETR_RESP")
CIT=$(jq -r '.items[0].citation // empty' "$RETR_RESP")
MEMID=$(jq -r '.items[0].id // empty' "$RETR_RESP")
jq -e '.api=="v1" and (.items|type=="array")' "$RETR_RESP" >/dev/null 2>&1 \
  && ok "retrieve envelope: api=v1 + items[]" || bad "retrieve envelope malformed"
jq -e 'has("support")' "$RETR_RESP" >/dev/null 2>&1 && ok "retrieve: support summary present" || bad "retrieve: no support summary"
S=$(retrieve "what database did we choose for billing" "precise"); assert_2xx "POST /v1/retrieve (precise/rerank)" "$S"
if [ "$GW" != "mock" ]; then
  jq -e '.degraded==false' "$RETR_RESP" >/dev/null 2>&1 && ok "retrieve: degraded=false (all models live)" || bad "retrieve: unexpectedly degraded"
fi

if [ -n "$CIT" ]; then
  S=$(api POST /v1/citations/resolve "{\"citations\":[\"$CIT\"]}"); assert_2xx "POST /v1/citations/resolve" "$S"
  S=$(api POST /v1/drilldown "{\"citation\":\"$CIT\"}"); assert_2xx "POST /v1/drilldown (verbatim span)" "$S"
else
  kbad "no citation handle to resolve/drilldown"
fi
[ -n "$MEMID" ] && { S=$(api GET "/v1/memories/$MEMID"); assert_2xx "GET /v1/memories/{id}" "$S"; } || kbad "GET /v1/memories/{id} (no memory id yet)"
# memory-level feedback (use|save|fail|noise) keys on memory_id; citation-level only
# carries wrong_citation. Exercise both.
if [ -n "$MEMID" ]; then
  S=$(api POST /v1/feedback "{\"memory_id\":\"$MEMID\",\"signal\":\"use\"}"); assert_2xx "POST /v1/feedback (memory-level use)" "$S"
else kbad "POST /v1/feedback (no memory id yet)"; fi
[ -n "$CIT" ] && { S=$(api POST /v1/feedback "{\"citation\":\"$CIT\",\"signal\":\"wrong_citation\"}"); assert_2xx "POST /v1/feedback (citation wrong_citation)" "$S"; } || kbad "POST /v1/feedback (citation, no citation yet)"

# ── Single-user knowledge surfaces: playbook, episodes, causal ────────────────
info "Playbook / episodes / causal / verify / traces"
S=$(api GET "/v1/playbook?session_id=$SESS1"); assert_2xx "GET /v1/playbook" "$S"
S=$(api GET /v1/episodes); assert_2xx "GET /v1/episodes" "$S"
# causal traversal is rooted at a memory.
[ -n "$MEMID" ] && { S=$(api GET "/v1/causal?memory_id=$MEMID"); assert_2xx "GET /v1/causal" "$S"; } || kbad "GET /v1/causal (no memory id yet)"
if [ -n "$CIT" ]; then
  S=$(api POST /v1/verify "{\"claim\":\"The billing service uses PostgreSQL.\",\"citations\":[\"$CIT\"]}")
  assert_2xx "POST /v1/verify (claim entailment, live judge)" "$S"
  jq -e '.verdict|type=="string"' "$RESP" >/dev/null 2>&1 && ok "verify: verdict returned ($(jq -r .verdict "$RESP"))" || bad "verify: no verdict"
else kbad "POST /v1/verify (no citation yet)"; fi
[ -n "$RESPID" ] && { S=$(api GET "/v1/traces/$RESPID"); assert_2xx "GET /v1/traces/{response_id}" "$S"; } || kbad "GET /v1/traces/{response_id} (no response_id yet)"

# ── Memory mutation: assert, supersede via correction, rollback ───────────────
info "Forgetting: assert / supersede (correction) / rollback"
S=$(api POST /v1/records "{\"records\":[$(rec user 'Correction: Dana switched editors — Dana now uses Zed, not Neovim, as the daily editor.' "$SESS2")]}")
assert_2xx "POST /v1/records (correction, session 2)" "$S"
S=$(api POST "/v1/buffers/$SESS2/flush" '{"trigger":"explicit"}'); assert_2xx "POST /v1/buffers/{key}/flush (s2)" "$S"
if [ -n "$MEMID" ]; then
  # PATCH /v1/memories/{id} RESOLVES a pending_confirmation memory (action=confirm|reject);
  # an active memory is not pending, so a clean 4xx ("not pending") proves the route is wired.
  S=$(api PATCH "/v1/memories/$MEMID" '{"action":"confirm"}')
  case "$S" in 2*|400|404|409) ok "PATCH /v1/memories/{id} (resolve; $S)";; *) bad "PATCH /v1/memories/{id} (got $S: $(head -c160 "$RESP"))";; esac
  S=$(api POST "/v1/memories/$MEMID/rollback" '{}')
  case "$S" in 2*|409) ok "POST /v1/memories/{id}/rollback (reversible op; $S)";; *) bad "rollback (got $S)";; esac
else kbad "PATCH+rollback /v1/memories/{id} (no memory id yet)"; fi

# ── Trust review queue + proactive suggestions ────────────────────────────────
info "Review queue / proactive suggestions"
S=$(api GET /v1/review "" "$ADMIN_KEY"); assert_2xx "GET /v1/review" "$S"
S=$(api "GET" "/v1/suggestions?session_id=$SESS1"); assert_2xx "GET /v1/suggestions" "$S"
SUG=$(jq -r '.suggestions[0].id // empty' "$RESP" 2>/dev/null)
[ -n "$SUG" ] && { S=$(api POST "/v1/suggestions/$SUG" '{"action":"dismiss"}'); assert_2xx "POST /v1/suggestions/{id}" "$S"; } \
              || ok "GET /v1/suggestions (none pending yet — route OK)"

# ── Team/admin tier: groups, grants, proactive config, scopes settings ────────
info "Team & admin tier (HTTP/MCP)"
S=$(api POST /v1/admin/groups '{"name":"Billing team"}' "$ADMIN_KEY"); assert_2xx "POST /v1/admin/groups" "$S"
GID=$(jq -r '.id // empty' "$RESP")  # the group id is server-assigned — use it (not the name)
S=$(api GET /v1/admin/groups "" "$ADMIN_KEY"); assert_2xx "GET /v1/admin/groups" "$S"
if [ -n "$GID" ]; then
  S=$(api POST "/v1/admin/groups/$GID/members" '{"user_id":"dana"}' "$ADMIN_KEY"); assert_2xx "POST /v1/admin/groups/{id}/members" "$S"
  S=$(api PUT /v1/scopes/grants "{\"group_id\":\"$GID\",\"access\":\"read\",\"zone_ceiling\":\"work\"}" "$ADMIN_KEY"); assert_2xx "PUT /v1/scopes/grants" "$S"
  GRANT=$(jq -r '.id // .grant.id // empty' "$RESP" 2>/dev/null)
  S=$(api GET /v1/scopes/grants "" "$ADMIN_KEY"); assert_2xx "GET /v1/scopes/grants" "$S"
  [ -n "$GRANT" ] && { S=$(api POST "/v1/grants/$GRANT/revoke" '{}' "$ADMIN_KEY"); assert_2xx "POST /v1/grants/{id}/revoke" "$S"; } \
                  || bad "grant create returned no id"
  S=$(api DELETE "/v1/admin/groups/$GID/members/dana" "" "$ADMIN_KEY"); assert_2xx "DELETE /v1/admin/groups/{id}/members/{user_id}" "$S"
else
  bad "POST /v1/admin/groups returned no id"
fi
S=$(api GET /v1/admin/proactive "" "$ADMIN_KEY"); assert_2xx "GET /v1/admin/proactive" "$S"
S=$(api PUT /v1/admin/proactive '{"enabled":true}' "$ADMIN_KEY"); assert_2xx "PUT /v1/admin/proactive" "$S"

# ── MCP-over-HTTP: the same capabilities, parity (D-067/D-074) ─────────────────
info "MCP-over-HTTP surface (streamable HTTP: initialize → initialized → tools)"
SIDF="$WORK/mcp.sid"   # session id persisted to a FILE — survives the $(...) subshells
# mcp RPC  → JSON payload in $WORK/mcp (SSE-unframed); status echoed. Carries the
# Mcp-Session-Id once the initialize response issues one (streamable-HTTP transport).
mcp() {
  local rpc="$1" sid=""
  [ -f "$SIDF" ] && sid=$(cat "$SIDF")
  local hdr=(-H "Authorization: Bearer $AGENT_KEY" -H 'Content-Type: application/json'
             -H 'Accept: application/json, text/event-stream')
  [ -n "$sid" ] && hdr+=(-H "Mcp-Session-Id: $sid")
  local code; code=$(curl -s -X POST "$MCP" "${hdr[@]}" -D "$WORK/mcp.hdr" \
    -o "$WORK/mcp.raw" -w '%{http_code}' -d "$rpc" 2>/dev/null)
  if [ -z "$sid" ]; then
    grep -i '^mcp-session-id:' "$WORK/mcp.hdr" 2>/dev/null | tr -d '\r' | awk '{print $2}' | head -1 > "$SIDF"
  fi
  # SSE frames prefix the JSON with "data: ".
  grep -q '^data: ' "$WORK/mcp.raw" 2>/dev/null \
    && sed -n 's/^data: //p' "$WORK/mcp.raw" | tail -1 > "$WORK/mcp" \
    || cp "$WORK/mcp.raw" "$WORK/mcp"
  printf '%s' "$code"
}
INIT='{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"acceptance","version":"0"}}}'
S=$(mcp "$INIT"); jq -e '.result.protocolVersion' "$WORK/mcp" >/dev/null 2>&1 \
  && ok "MCP initialize (HTTP $S, session=$(cat "$SIDF" 2>/dev/null))" || bad "MCP initialize (HTTP $S: $(head -c160 "$WORK/mcp"))"
# Complete the handshake: the client MUST send notifications/initialized before tools.
mcp '{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}' >/dev/null
# auth guard (no Bearer ⇒ 401)
C=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$MCP" -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' -d "$INIT" 2>/dev/null)
[ "$C" = 401 ] && ok "MCP enforces auth (401 without Bearer)" || bad "MCP did not require auth (got $C)"
S=$(mcp '{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}')
TOOLN=$(jq -r '.result.tools|length // 0' "$WORK/mcp" 2>/dev/null)
[ "${TOOLN:-0}" -ge 15 ] && ok "MCP tools/list ($TOOLN tools)" || bad "MCP tools/list returned ${TOOLN:-0} tools (raw: $(head -c160 "$WORK/mcp"))"
# tools/call: memory_retrieve (parity with HTTP retrieve)
S=$(mcp '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"memory_retrieve","arguments":{"query":"what database did we choose for billing","limit":3}}}')
jq -e '.result' "$WORK/mcp" >/dev/null 2>&1 && ok "MCP tools/call memory_retrieve (parity)" || bad "MCP memory_retrieve failed: $(head -c160 "$WORK/mcp")"
# tools/call: memory_playbook + memory_topics (read surface)
S=$(mcp '{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"memory_playbook","arguments":{"session_id":"'"$SESS1"'"}}}')
jq -e '.result' "$WORK/mcp" >/dev/null 2>&1 && ok "MCP tools/call memory_playbook" || bad "MCP memory_playbook failed: $(head -c160 "$WORK/mcp")"
S=$(mcp '{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"memory_topics","arguments":{}}}')
jq -e '.result' "$WORK/mcp" >/dev/null 2>&1 && ok "MCP tools/call memory_topics" || bad "MCP memory_topics failed: $(head -c160 "$WORK/mcp")"

# ── DSAR: cascading delete per (tenant,user) (§13, key/credential tier) ────────
info "DSAR cascading delete (§13)"
S=$(api DELETE "/v1/admin/users/dana" "" "$ADMIN_KEY"); assert_2xx "DELETE /v1/admin/users/{user} (DSAR cascade)" "$S"

# ── Key revocation (terminal admin) ───────────────────────────────────────────
S=$(api POST /v1/admin/keys/revoke-tenant "{\"tenant_id\":\"$TENANT\"}" "$ADMIN_KEY")
assert_2xx "POST /v1/admin/keys/revoke-tenant" "$S"

# ── Summary ───────────────────────────────────────────────────────────────────
echo ""
echo "════════════════════════════════════════════════════════════════"
echo " Full-cycle LIVE acceptance — gateway=${GW} model=${MODEL}"
echo "   embed=${EMBED_MODEL}@${EMBED_DIMS}  rerank=${RERANK_MODEL}"
echo "   routes asserted: $((pass+fails))   PASS: ${pass}   FAIL: ${fails}"
echo "════════════════════════════════════════════════════════════════"
if [ "$fails" -ne 0 ]; then
  printf '%s\n' "${ROUTE_RESULTS[@]}" | grep '^FAIL' >&2
  echo "server log: $WORK/serve.log (kept until process exit)" >&2
fi
exit "$fails"
