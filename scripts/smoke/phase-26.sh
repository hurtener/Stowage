#!/usr/bin/env bash
# Phase 26 smoke: reasoning traces + audit export (RFC §6c). D-086.
#
#   AC-1  capture wired: retrieve.query (async, via injection writer) + verify.verdict.
#   AC-2  traces.Reconstruct + sign are gateway-free; signing is ed25519 stdlib.
#   AC-3  memory_trace on HTTP route + MCP tool (+ golden, count 18) + SDK client.
#   AC-4  trace.signing_key config knob (secret-indirected, validated).
#   AC    unit + parity + fuzz tests pass; goldens stable; eval-ci green.
set -uo pipefail
cd "$(dirname "$0")/../.."

fails=0
ok()    { printf 'OK   %s\n' "$*"; }
failc() { printf 'FAIL %s\n' "$*"; fails=$((fails+1)); }
skip()  { printf 'SKIP %s\n' "$*"; }

if [ ! -f internal/traces/reconstruct.go ]; then
  skip "AC-1..4: reasoning traces not yet implemented (plan skeleton)"
  exit "$fails"
fi

# ── AC-1: capture wired ──────────────────────────────────────────────────────────
if grep -q 'retrieve.query' internal/retrieval/retrieval.go 2>/dev/null \
   && grep -q 'SetEventStore' internal/retrieval/injections.go 2>/dev/null; then
  ok "AC-1: retrieve.query event capture wired (async injection writer)"
else
  failc "AC-1: retrieve.query capture missing"
fi
if grep -q 'verify.verdict' internal/trust/verify.go 2>/dev/null \
   && grep -q 'func VerifyClaim' internal/trust/verify.go 2>/dev/null; then
  ok "AC-1: verify.verdict event capture wired (VerifyClaim core)"
else
  failc "AC-1: verify.verdict capture missing"
fi

# ── AC-2: traces core gateway-free + ed25519 ─────────────────────────────────────
if grep -qE '"github.com/hurtener/stowage/internal/gateway"' internal/traces/reconstruct.go internal/traces/sign.go 2>/dev/null; then
  failc "AC-2: traces core imports the gateway (must be deterministic/LLM-free)"
else
  ok "AC-2: traces.Reconstruct + sign are gateway-free"
fi
if grep -q 'crypto/ed25519' internal/traces/sign.go 2>/dev/null; then
  ok "AC-2: trace signing uses ed25519 (CGo-free stdlib)"
else
  failc "AC-2: ed25519 signing missing"
fi

# ── AC-3: memory_trace on all three surfaces (D-067) ─────────────────────────────
if grep -q 'GET /v1/traces/{response_id}' internal/api/server.go 2>/dev/null; then
  ok "AC-3: GET /v1/traces/{response_id} route registered"
else
  failc "AC-3: trace HTTP route missing"
fi
if grep -q 'memory_trace' internal/mcpserver/server.go 2>/dev/null \
   && [ -f internal/mcpserver/testdata/memory_trace.output.schema.json ]; then
  ok "AC-3: memory_trace MCP tool registered + schema golden present"
else
  failc "AC-3: memory_trace tool or golden missing"
fi
if grep -q 'Trace(ctx context.Context, req TraceRequest)' sdk/stowage/client.go 2>/dev/null; then
  ok "AC-3: SDK Client.Trace present"
else
  failc "AC-3: SDK Trace missing"
fi

# ── AC-4: config knob ────────────────────────────────────────────────────────────
if grep -q 'trace.signing_key' internal/config/config.go 2>/dev/null \
   && grep -q 'must use env.VAR indirection' internal/config/config.go 2>/dev/null; then
  ok "AC-4: trace.signing_key knob present + secret-indirection validated (D-030)"
else
  failc "AC-4: trace.signing_key knob/validation missing"
fi

# ── AC: unit + parity + fuzz ─────────────────────────────────────────────────────
if CGO_ENABLED=1 go test -count=1 -timeout=180s ./internal/traces/ >/tmp/p26-traces.log 2>&1; then
  ok "AC-2: traces unit + fuzz-corpus tests pass"
else
  failc "AC-2: traces tests failed"; tail -25 /tmp/p26-traces.log >&2
fi
if CGO_ENABLED=1 go test -count=1 -timeout=180s -run 'TestRetrieveQueryCaptured' ./internal/retrieval/ >/tmp/p26-cap.log 2>&1 \
   && CGO_ENABLED=1 go test -count=1 -timeout=180s -run 'TestVerifyClaim_CapturesVerdict' ./internal/trust/ >/tmp/p26-vcap.log 2>&1; then
  ok "AC-1: capture tests (retrieve.query + verify.verdict) pass"
else
  failc "AC-1: capture tests failed"; tail -25 /tmp/p26-cap.log /tmp/p26-vcap.log >&2
fi
if CGO_ENABLED=1 go test -count=1 -timeout=300s -run 'TestTraceParity' ./test/integration/ >/tmp/p26-parity.log 2>&1; then
  ok "AC-3: memory_trace all-surfaces parity passes"
else
  failc "AC-3: trace parity failed"; tail -25 /tmp/p26-parity.log >&2
fi

# ── AC: MCP goldens + tool count + eval gate ─────────────────────────────────────
if CGO_ENABLED=1 go test -count=1 -run 'TestSchemaGoldens|TestNew_AllToolsRegistered|TestServer' ./internal/mcpserver/ >/tmp/p26-golden.log 2>&1; then
  ok "AC-3: MCP goldens stable + tool count = 18 (incl. memory_trace)"
else
  failc "AC-3: MCP goldens/tool-count drifted"; cat /tmp/p26-golden.log >&2
fi
if make eval-ci >/tmp/p26-evalci.log 2>&1; then
  ok "AC: make eval-ci green (deterministic CI unaffected)"
else
  failc "AC: make eval-ci failed"; cat /tmp/p26-evalci.log >&2
fi

exit "$fails"
