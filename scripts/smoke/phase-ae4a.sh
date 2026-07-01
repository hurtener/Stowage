#!/usr/bin/env bash
# Phase ae4a smoke: lean MCP read (Text markdown + episode hook + drill by citation
# ULID), D-142. memory_retrieve returns ae3's Render(RenderMCP,…) body in the
# model-facing Text; episode hook from already-loaded Memory.EpisodeID (no new store
# query); drill handle = existing citation ULID (no (response_id,rank) method — H1);
# same body exposed on HTTP/SDK as `rendered`; token win is context-only, not wire (M4).
#
# Verifies:
#   AC-1  retrieval.RenderReadBody defined; MCP retrieve Text no longer count-only.
#   AC-3  no positional (response_id,rank) / new InjectionStore drill method (H1).
#   AC-4  memory_retrieve Describe() states the M4 wire-truth.
#   AC-5  `rendered` field present on HTTP retrieveResponse + SDK RetrieveResponse.
#   AC-8  no RenderMode/render knob leaked into internal/config; render + handler tests pass.
set -uo pipefail
cd "$(dirname "$0")/../.."

fails=0
ok()    { printf 'OK   %s\n' "$*"; }
failc() { printf 'FAIL %s\n' "$*"; fails=$((fails+1)); }
skip()  { printf 'SKIP %s\n' "$*"; }

RENDER=internal/retrieval/render.go
HANDLERS=internal/mcpserver/handlers.go
SERVER=internal/mcpserver/server.go

# ── AC-1: the composition helper exists (ae3's render.go extended by ae4a) ───────
if [ ! -f "$RENDER" ]; then
  skip "AC-1: $RENDER not built yet (ae3 not landed)"
  exit "$fails"
fi
if grep -Eq 'func RenderReadBody\(' "$RENDER"; then
  ok "AC-1: $RENDER defines RenderReadBody"
else
  skip "AC-1: RenderReadBody not built yet (ae4a not landed)"
  exit "$fails"
fi

# ── AC-1: MCP retrieve Text flipped off the count-only string ───────────────────
if grep -q 'RenderReadBody' "$HANDLERS" && ! grep -q 'Retrieved %d item(s)' "$HANDLERS"; then
  ok "AC-1: MCP retrieve Text is the rendered body (count-only string removed)"
else
  failc "AC-1: MCP retrieve Text not flipped to RenderReadBody"
fi

# ── AC-3: no positional (response_id,rank) drill method added (H1) ──────────────
if grep -REiq 'func .*ListByResponse.*rank|DrillByRank|ByResponseRank|response_id.*rank.*drill' internal/store internal/mcpserver 2>/dev/null; then
  failc "AC-3: a positional (response_id,rank) drill method appears (H1 forbids it in ae4a)"
else
  ok "AC-3: no positional (response_id,rank) drill method (H1 deferred to ae4b)"
fi

# ── AC-4: the tool doc states the M4 wire-truth ─────────────────────────────────
if grep -Eiq 'payload grows|context, not the wire|not the wire payload|larger payload' "$SERVER"; then
  ok "AC-4: memory_retrieve Describe() states the M4 wire-truth"
else
  failc "AC-4: memory_retrieve Describe() missing the M4 wire-truth statement"
fi

# ── AC-5: `rendered` field present on HTTP + SDK response types ──────────────────
if grep -q 'json:"rendered' internal/api/retrieve_handler.go && grep -q 'json:"rendered' sdk/stowage/types.go; then
  ok "AC-5: rendered field present on HTTP retrieveResponse + SDK RetrieveResponse"
else
  failc "AC-5: rendered field missing from an HTTP/SDK response type (parity)"
fi

# ── AC-8: no config knob leaked; render + handler tests pass ─────────────────────
if grep -Rq 'RenderReadBody\|RenderMode' internal/config 2>/dev/null; then
  failc "AC-8: render symbol appears in internal/config (ae4a adds no knob)"
else
  ok "AC-8: no render config knob added (ae4a is knobless)"
fi

if go test ./internal/retrieval/ -run Render >/dev/null 2>&1; then
  ok "AC-6/8: internal/retrieval Render tests pass"
else
  failc "AC-6/8: internal/retrieval Render tests fail"
fi

if go test ./internal/mcpserver/ -run Retrieve >/dev/null 2>&1; then
  ok "AC-1/2: internal/mcpserver Retrieve tests pass"
else
  failc "AC-1/2: internal/mcpserver Retrieve tests fail"
fi

exit "$fails"
