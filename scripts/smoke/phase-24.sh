#!/usr/bin/env bash
# Phase 24 smoke: causal links (RFC §5.6/§6b). D-083.
#
#   AC-1  causal.Infer is schema-constrained via the gateway seam (P5/D-040).
#   AC-2  narration wires inference (gather decisions → infer → gate → CommitSet.Links).
#   AC-3  causal.Traverse is gateway-free (no internal/gateway import in traverse.go).
#   AC-4  ListMemoriesByRecords on the store seam (both drivers) + conformance.
#   AC-5  memory_causal on HTTP route + MCP tool (+ golden) + SDK client; tool count = 15.
#   AC    unit + parity + conformance + lifecycle tests pass; goldens stable; eval-ci green.
set -uo pipefail
cd "$(dirname "$0")/../.."

fails=0
ok()    { printf 'OK   %s\n' "$*"; }
failc() { printf 'FAIL %s\n' "$*"; fails=$((fails+1)); }
skip()  { printf 'SKIP %s\n' "$*"; }

if [ ! -f internal/causal/traverse.go ]; then
  skip "AC-1..5: causal links not yet implemented (plan skeleton)"
  exit "$fails"
fi

# ── AC-1: inference is gateway-schema-constrained ────────────────────────────────
if grep -q 'gw.Complete' internal/causal/infer.go 2>/dev/null \
   && grep -q 'Schema:' internal/causal/infer.go 2>/dev/null; then
  ok "AC-1: causal.Infer uses a schema-constrained gateway Complete (P5/D-040)"
else
  failc "AC-1: causal.Infer not schema-constrained"
fi

# ── AC-2: narration wires inference ──────────────────────────────────────────────
if grep -q 'inferCausalLinks' internal/lifecycle/episodes.go 2>/dev/null \
   && grep -q 'causal.Infer' internal/lifecycle/episodes.go 2>/dev/null \
   && grep -q 'causal.GateProposals' internal/lifecycle/episodes.go 2>/dev/null; then
  ok "AC-2: narration sweep gathers decisions → infers → gates → commits links"
else
  failc "AC-2: causal inference not wired into narration"
fi

# ── AC-3: traversal is gateway-free (P5) ─────────────────────────────────────────
if grep -qE '"github.com/hurtener/stowage/internal/gateway"' internal/causal/traverse.go 2>/dev/null; then
  failc "AC-3: traverse.go imports the gateway (the why-traversal must stay LLM-free)"
else
  ok "AC-3: causal.Traverse is gateway-free"
fi

# ── AC-4: store seam reverse-provenance method on both drivers ────────────────────
if grep -q 'func (m \*memoryStore) ListMemoriesByRecords' internal/store/sqlitestore/memories.go 2>/dev/null \
   && grep -q 'func (m \*memoryStore) ListMemoriesByRecords' internal/store/pgstore/memories.go 2>/dev/null \
   && grep -q 'ListMemoriesByRecords' internal/store/conformance/conformance.go 2>/dev/null; then
  ok "AC-4: ListMemoriesByRecords on sqlite + pgx + conformance"
else
  failc "AC-4: ListMemoriesByRecords missing on a driver or conformance"
fi

# ── AC-5: memory_causal on all three single-user surfaces (D-067) ─────────────────
if grep -q 'GET /v1/causal' internal/api/server.go 2>/dev/null; then
  ok "AC-5: GET /v1/causal route registered"
else
  failc "AC-5: GET /v1/causal route missing"
fi
if grep -q 'memory_causal' internal/mcpserver/server.go 2>/dev/null \
   && [ -f internal/mcpserver/testdata/memory_causal.output.schema.json ]; then
  ok "AC-5: memory_causal MCP tool registered + schema golden present"
else
  failc "AC-5: memory_causal tool or schema golden missing"
fi
if grep -q 'Causal(ctx context.Context, req CausalRequest)' sdk/stowage/client.go 2>/dev/null; then
  ok "AC-5: SDK Client.Causal present"
else
  failc "AC-5: SDK Client.Causal missing"
fi

# ── AC: unit + parity + conformance + lifecycle ──────────────────────────────────
if CGO_ENABLED=1 go test -count=1 -timeout=300s ./internal/causal/ >/tmp/p24-causal.log 2>&1; then
  ok "AC-1/3: causal unit tests (infer + traverse) pass"
else
  failc "AC-1/3: causal unit tests failed"; tail -25 /tmp/p24-causal.log >&2
fi
if CGO_ENABLED=1 go test -count=1 -timeout=300s -run 'TestEpisodeSweeps_Causal' ./internal/lifecycle/ >/tmp/p24-lc.log 2>&1; then
  ok "AC-2: causal inference lifecycle tests pass"
else
  failc "AC-2: causal lifecycle tests failed"; tail -25 /tmp/p24-lc.log >&2
fi
if CGO_ENABLED=1 go test -count=1 -timeout=300s -run 'TestCausalParity' ./test/integration/ >/tmp/p24-parity.log 2>&1; then
  ok "AC-5: memory_causal all-surfaces parity passes"
else
  failc "AC-5: causal parity failed"; tail -25 /tmp/p24-parity.log >&2
fi
if CGO_ENABLED=1 go test -count=1 -run 'TestConformance|MemoryListByRecords' ./internal/store/sqlitestore/ >/tmp/p24-conf.log 2>&1; then
  ok "AC-4: store conformance (incl. ListMemoriesByRecords) passes on sqlite"
else
  failc "AC-4: store conformance failed"; tail -25 /tmp/p24-conf.log >&2
fi

# ── AC: MCP schema goldens stable + tool count + CI eval gate unaffected ──────────
if CGO_ENABLED=1 go test -count=1 -run 'TestSchemaGoldens|TestServer' ./internal/mcpserver/ >/tmp/p24-golden.log 2>&1; then
  ok "AC-5: MCP schema goldens stable + tool count = 15 (incl. memory_causal)"
else
  failc "AC-5: MCP goldens/tool-count drifted"; cat /tmp/p24-golden.log >&2
fi
if make eval-ci >/tmp/p24-evalci.log 2>&1; then
  ok "AC: make eval-ci green (deterministic CI unaffected)"
else
  failc "AC: make eval-ci failed"; cat /tmp/p24-evalci.log >&2
fi

exit "$fails"
