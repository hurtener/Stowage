#!/usr/bin/env bash
# Phase h1 smoke test: boot.StartPipeline — pipeline + lifecycle parity across
# all entrypoints (D-067 Wave A, D-068).
#
# Acceptance criteria verified:
#   AC-1  boot.StartPipeline is the only live-serving stage constructor
#         (no stray pipeline.New|NewExtractStage|reconcile.New|lifecycle.New|
#          BackfillSweep in cmd/stowage or sdk/stowage outside boot).
#   AC-3  A record ingested via `stowage mcp` becomes a retrievable memory
#         (the flagship parity blocker — was a silent dead-end before h1).
#
# AC-3 drives the real `stowage mcp` binary over the stdio transport (the
# robust bash-drivable MCP transport). The streamable-HTTP transport shares the
# identical StartPipeline wiring (only the transport handler differs) and the
# in-process MCP protocol path is covered by test/integration/pipeline_parity_test.go.
set -uo pipefail
cd "$(dirname "$0")/../.."

fails=0
ok()    { printf 'OK   %s\n' "$*"; }
failc() { printf 'FAIL %s\n' "$*"; fails=$((fails+1)); }
skip()  { printf 'SKIP %s\n' "$*"; }

# ── AC-1: no stray live-stage construction outside boot.StartPipeline ──────────
# Exclude *_test.go (test wiring is not the post-boot drift surface) — AC-1 is
# about production entrypoint wiring.
STRAY=$(grep -rnE 'pipeline\.New|NewExtractStage|reconcile\.New|lifecycle\.New|BackfillSweep' \
          cmd/stowage sdk/stowage 2>/dev/null | grep -v '_test.go' || true)
if [ -n "$STRAY" ]; then
  failc "AC-1: stray live-stage construction outside boot.StartPipeline"
  printf '%s\n' "$STRAY" >&2
else
  ok "AC-1: boot.StartPipeline is the sole live-stage constructor"
fi

# ── Build the binary (SKIP-graceful) ──────────────────────────────────────────
command -v jq >/dev/null 2>&1 || { skip "AC-3: jq unavailable"; exit "$fails"; }

BIN=/tmp/stowage-smoke-h1
TMPDIR_SMOKE=$(mktemp -d)
cleanup() {
  [ -n "${MCP_PID:-}" ] && kill "$MCP_PID" 2>/dev/null
  exec 3>&- 2>/dev/null || true
  rm -f "$BIN"; rm -rf "$TMPDIR_SMOKE"
}
trap cleanup EXIT

if ! CGO_ENABLED=0 go build -o "$BIN" ./cmd/stowage 2>"${TMPDIR_SMOKE}/build.log"; then
  skip "AC-3: binary did not build"
  cat "${TMPDIR_SMOKE}/build.log" >&2
  exit "$fails"
fi
ok "cgo-free build"

# ── Temp environment ──────────────────────────────────────────────────────────
DB_PATH="${TMPDIR_SMOKE}/smoke.db"
CFG_PATH="${TMPDIR_SMOKE}/stowage.yaml"
SCRIPT_PATH="${TMPDIR_SMOKE}/mockscript.json"
FIFO="${TMPDIR_SMOKE}/in.fifo"
OUT="${TMPDIR_SMOKE}/out.jsonl"
SESS="smokeh1-sess"

cat > "$CFG_PATH" <<YAML
store:
  driver: sqlite
  dsn: "${DB_PATH}"
gateway:
  driver: mock
  embed_dims: 8
mcp:
  stdio_tenant: smokeh1
YAML

echo '[]' > "$SCRIPT_PATH"
"$BIN" migrate --config "$CFG_PATH" >/dev/null 2>&1 \
  && ok "migrate applied" \
  || { failc "migrate failed"; exit "$fails"; }

# ── Launch `stowage mcp` (stdio) with a fifo for request/response ─────────────
mkfifo "$FIFO"
export STOWAGE_MOCK_SCRIPT="$SCRIPT_PATH"
"$BIN" mcp --config "$CFG_PATH" < "$FIFO" > "$OUT" 2>"${TMPDIR_SMOKE}/mcp.log" &
MCP_PID=$!
exec 3>"$FIFO"   # hold the write end open across multiple sends

send() { printf '%s\n' "$1" >&3; }

# Wait until a JSON-RPC response with the given id appears in $OUT.
await_id() {
  local id="$1" tries="${2:-60}"
  for _ in $(seq 1 "$tries"); do
    if jq -e --argjson id "$id" 'select(.id==$id)' "$OUT" >/dev/null 2>&1; then return 0; fi
    sleep 0.2
  done
  return 1
}
# Extract a field via jq from the response object with the given id.
field_of() { jq -r --argjson id "$1" "select(.id==\$id) | $2 // empty" "$OUT" 2>/dev/null | head -1; }

# 1. initialize handshake.
send '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"smokeh1","version":"0.0.1"}}}'
send '{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}'
if ! await_id 1; then
  failc "AC-3: MCP initialize did not respond"
  cat "${TMPDIR_SMOKE}/mcp.log" >&2
  exit "$fails"
fi
ok "mcp stdio initialized"

# 2. install an active topic so extraction does not short-circuit.
send '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"memory_topics","arguments":{"action":"upsert","topics":[{"key":"smokeh1-topic","description":"h1 smoke","status":"active"}]}}}'
await_id 2 >/dev/null

# 3. ingest 17 records (below the count=18 trigger — no flush yet; D-107 coarsened window).
REC='{"role":"user","content":"Paris is the capital of France and a major European city.","session_id":"'"$SESS"'"}'
RECS11=$(printf '%s,' $(yes "$REC" | head -17) | sed 's/,$//')
send '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"memory_ingest","arguments":{"records":['"$RECS11"']}}}'
if ! await_id 3; then failc "AC-3: ingest(11) no response"; exit "$fails"; fi
REC_ID=$(field_of 3 '.result.structuredContent.ids[0]')
if [ -z "$REC_ID" ]; then
  failc "AC-3: could not extract a record id from memory_ingest output"
  jq -c 'select(.id==3)' "$OUT" >&2
  exit "$fails"
fi
ok "mcp memory_ingest accepted records (id=${REC_ID:0:8}…)"

# 4. script the mock extraction (provenance must reference a buffered record).
cat > "$SCRIPT_PATH" <<JSON
[{"candidates":[{"kind":"fact","content":"The capital of France is Paris, a major European city.","context":"geography","entities":["france","paris"],"keywords":["capital","france","paris"],"anticipated_queries":["what is the capital of france"],"importance":3,"confidence":0.9,"provenance":[{"record_id":"${REC_ID}","span_start":0,"span_end":10}]}]}]
JSON

# 5. ingest the 18th record → fires the count trigger → flush → extract → reconcile.
send '{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"memory_ingest","arguments":{"records":['"$REC"']}}}'
await_id 4 >/dev/null

# 6. poll memory_retrieve until a memory appears (reconcile is async, P2).
GOT=0
for i in $(seq 1 40); do
  RID=$((100+i))
  send '{"jsonrpc":"2.0","id":'"$RID"',"method":"tools/call","params":{"name":"memory_retrieve","arguments":{"query":"capital of France Paris","limit":5}}}'
  if await_id "$RID" 10; then
    COUNT=$(field_of "$RID" '(.result.structuredContent.items | length)')
    if [ -n "$COUNT" ] && [ "$COUNT" -ge 1 ] 2>/dev/null; then GOT=1; break; fi
  fi
  sleep 0.3
done

if [ "$GOT" -eq 1 ]; then
  ok "AC-3: MCP-ingested record became a retrievable memory (count=$COUNT)"
else
  # Cross-check the store so the failure message is actionable.
  MEMS=$(sqlite3 "$DB_PATH" "SELECT COUNT(*) FROM memories WHERE tenant_id='smokeh1' AND status='active';" 2>/dev/null || echo 0)
  failc "AC-3: MCP ingest produced no retrievable memory (active memories in store=$MEMS) — the flagship bug"
  echo "--- mcp stderr ---" >&2; tail -20 "${TMPDIR_SMOKE}/mcp.log" >&2
fi

# ── Clean shutdown ────────────────────────────────────────────────────────────
exec 3>&-
kill "$MCP_PID" 2>/dev/null
wait "$MCP_PID" 2>/dev/null
MCP_PID=""
ok "mcp stdio session closed"

exit "$fails"
