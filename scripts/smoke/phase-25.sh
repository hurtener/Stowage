#!/usr/bin/env bash
# Phase 25 smoke: verification & review queue (RFC §6c). D-084.
#
#   AC-1  trust.Verify is schema-constrained via the gateway seam (P5/D-040), degraded-safe.
#   AC-2  memory_assert review flag parks pending_review; review.go is gateway-free.
#   AC-3  memory_verify + memory_review on HTTP + MCP + SDK; tool count = 17.
#   AC    unit + parity tests pass; MCP goldens stable; eval-ci green.
set -uo pipefail
cd "$(dirname "$0")/../.."

fails=0
ok()    { printf 'OK   %s\n' "$*"; }
failc() { printf 'FAIL %s\n' "$*"; fails=$((fails+1)); }
skip()  { printf 'SKIP %s\n' "$*"; }

if [ ! -f internal/trust/verify.go ]; then
  skip "AC-1..3: verification/review not yet implemented (plan skeleton)"
  exit "$fails"
fi

# ── AC-1: verify is gateway-schema-constrained ───────────────────────────────────
if grep -q 'gw.Complete' internal/trust/verify.go 2>/dev/null \
   && grep -q 'Schema:' internal/trust/verify.go 2>/dev/null; then
  ok "AC-1: trust.Verify uses a schema-constrained gateway Complete (P5/D-040)"
else
  failc "AC-1: trust.Verify not schema-constrained"
fi

# ── AC-2: review.go gateway-free + assert review flag ────────────────────────────
if grep -qE '"github.com/hurtener/stowage/internal/gateway"' internal/trust/review.go 2>/dev/null; then
  failc "AC-2: review.go imports the gateway (the review queue must stay LLM-free)"
else
  ok "AC-2: trust review core is gateway-free"
fi
if grep -q 'pending_review' internal/reconcile/assert.go 2>/dev/null \
   && grep -q 'Review' internal/reconcile/assert.go 2>/dev/null; then
  ok "AC-2: memory_assert review flag parks pending_review"
else
  failc "AC-2: assert review→pending_review wiring missing"
fi

# ── AC-3: verify + review on all surfaces (D-067) ────────────────────────────────
if grep -q 'POST /v1/verify' internal/api/server.go 2>/dev/null \
   && grep -q 'GET /v1/review' internal/api/server.go 2>/dev/null; then
  ok "AC-3: /v1/verify + /v1/review routes registered"
else
  failc "AC-3: HTTP verify/review routes missing"
fi
if grep -q 'memory_verify' internal/mcpserver/server.go 2>/dev/null \
   && grep -q 'memory_review' internal/mcpserver/server.go 2>/dev/null \
   && [ -f internal/mcpserver/testdata/memory_verify.output.schema.json ] \
   && [ -f internal/mcpserver/testdata/memory_review.output.schema.json ]; then
  ok "AC-3: memory_verify + memory_review MCP tools + schema goldens present"
else
  failc "AC-3: MCP verify/review tools or goldens missing"
fi
if grep -q 'Verify(ctx context.Context, req VerifyRequest)' sdk/stowage/client.go 2>/dev/null \
   && grep -q 'Review(ctx context.Context, req ReviewRequest)' sdk/stowage/client.go 2>/dev/null; then
  ok "AC-3: SDK Client.Verify + Client.Review present"
else
  failc "AC-3: SDK Verify/Review missing"
fi

# ── AC: unit + parity tests ──────────────────────────────────────────────────────
if CGO_ENABLED=1 go test -count=1 -timeout=300s ./internal/trust/ >/tmp/p25-trust.log 2>&1; then
  ok "AC-1/2: trust unit tests (verify + review) pass"
else
  failc "AC-1/2: trust unit tests failed"; tail -25 /tmp/p25-trust.log >&2
fi
if CGO_ENABLED=1 go test -count=1 -timeout=300s -run 'TestVerifyParity|TestReviewParity' ./test/integration/ >/tmp/p25-parity.log 2>&1; then
  ok "AC-3: verify + review all-surfaces parity passes"
else
  failc "AC-3: verify/review parity failed"; tail -25 /tmp/p25-parity.log >&2
fi

# ── AC: MCP schema goldens stable + tool count + CI eval gate unaffected ──────────
if CGO_ENABLED=1 go test -count=1 -run 'TestSchemaGoldens|TestNew_AllToolsRegistered|TestServer' ./internal/mcpserver/ >/tmp/p25-golden.log 2>&1; then
  ok "AC-3: MCP schema goldens stable + tool count = 17 (incl. memory_verify, memory_review)"
else
  failc "AC-3: MCP goldens/tool-count drifted"; cat /tmp/p25-golden.log >&2
fi
if make eval-ci >/tmp/p25-evalci.log 2>&1; then
  ok "AC: make eval-ci green (deterministic CI unaffected)"
else
  failc "AC: make eval-ci failed"; cat /tmp/p25-evalci.log >&2
fi

exit "$fails"
