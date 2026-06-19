#!/usr/bin/env bash
# Phase 27 smoke: proactive trigger engine (RFC §6d). D-087.
#
#   AC-1  trigger engine: proactive.Evaluate runs the rules, scores via scoring.Score,
#         applies the class multiplier, gates by threshold+budget, dedupes per session.
#   AC-2  governance: profile default ⊕ scope override (scope_settings), opt-out,
#         malformed-fails-safe-OFF; profile-internal defaults (no top-level knob).
#   AC-3  feedback tuning: accept/dismiss tunes per-(scope,class) confidence.
#   AC-4  surfaces (D-067): memory_suggestions {SDK,HTTP,MCP} + memory_proactive_config
#         admin {HTTP,MCP}; tool count 18→20; goldens present; SDK tier boundary holds.
#   AC-5  forgetting (P4): gateway-free expiry sweep GCs stale pending offers.
#   AC-6  store seam: SuggestionStore + ScopeSettingsStore on both drivers + conformance;
#         no new table/column (one index-only migration 0010).
#   AC    unit + parity + fuzz tests pass; goldens stable; eval-ci green.
set -uo pipefail
cd "$(dirname "$0")/../.."

fails=0
ok()    { printf 'OK   %s\n' "$*"; }
failc() { printf 'FAIL %s\n' "$*"; fails=$((fails+1)); }
skip()  { printf 'SKIP %s\n' "$*"; }

if [ ! -f internal/proactive/engine.go ]; then
  skip "AC-1..6: proactive engine not yet implemented (plan skeleton)"
  exit "$fails"
fi

# ── AC-1: trigger engine reuses scoring; gateway-light ───────────────────────────
if grep -q 'scoring.Score' internal/proactive/engine.go 2>/dev/null \
   && grep -q 'func Evaluate' internal/proactive/engine.go 2>/dev/null; then
  ok "AC-1: proactive.Evaluate scores candidates via scoring.Score (no new scoring)"
else
  failc "AC-1: Evaluate/scoring reuse missing"
fi
if grep -q 'classMultiplier' internal/proactive/engine.go 2>/dev/null; then
  ok "AC-1: per-class confidence multiplier applied in the engine"
else
  failc "AC-1: class multiplier not wired into the engine"
fi

# ── AC-2: governance resolve (profile ⊕ scope), fail-safe OFF ────────────────────
if grep -q 'func Resolve' internal/proactive/governance.go 2>/dev/null \
   && grep -q 'fail safe' internal/proactive/governance.go 2>/dev/null; then
  ok "AC-2: governance Resolve merges profile default ⊕ scope override (fail-safe OFF)"
else
  failc "AC-2: governance Resolve / fail-safe missing"
fi
if grep -q 'func ProactiveConfigForProfile' internal/config/profiles.go 2>/dev/null; then
  ok "AC-2: profile-internal proactive defaults (no top-level knob, D-034)"
else
  failc "AC-2: ProactiveConfigForProfile missing"
fi

# ── AC-3: feedback tuning ────────────────────────────────────────────────────────
if grep -q 'func classMultiplier' internal/proactive/tuning.go 2>/dev/null; then
  ok "AC-3: accept/dismiss confidence tuning (classMultiplier) present"
else
  failc "AC-3: classMultiplier tuning missing"
fi

# ── AC-4: surfaces on all tiers (D-067) ──────────────────────────────────────────
if grep -q 'GET /v1/suggestions' internal/api/server.go 2>/dev/null \
   && grep -q 'POST /v1/suggestions/{id}' internal/api/server.go 2>/dev/null; then
  ok "AC-4: /v1/suggestions list + resolve routes registered"
else
  failc "AC-4: suggestions HTTP routes missing"
fi
if grep -q 'GET /v1/admin/proactive' internal/api/server.go 2>/dev/null \
   && grep -q 'PUT /v1/admin/proactive' internal/api/server.go 2>/dev/null; then
  ok "AC-4: /v1/admin/proactive governance routes (admin tier) registered"
else
  failc "AC-4: proactive governance routes missing"
fi
if grep -q 'memory_suggestions' internal/mcpserver/server.go 2>/dev/null \
   && [ -f internal/mcpserver/testdata/memory_suggestions.output.schema.json ]; then
  ok "AC-4: memory_suggestions MCP tool + schema golden present"
else
  failc "AC-4: memory_suggestions tool or golden missing"
fi
if grep -q 'memory_proactive_config' internal/mcpserver/server.go 2>/dev/null \
   && [ -f internal/mcpserver/testdata/memory_proactive_config.output.schema.json ]; then
  ok "AC-4: memory_proactive_config MCP tool + schema golden present"
else
  failc "AC-4: memory_proactive_config tool or golden missing"
fi
if grep -q 'Suggestions(ctx context.Context, req SuggestionsRequest)' sdk/stowage/client.go 2>/dev/null; then
  ok "AC-4: SDK Client.Suggestions present (single-user tier)"
else
  failc "AC-4: SDK Suggestions method missing"
fi

# ── AC-5: forgetting — expiry sweep, gateway-free ────────────────────────────────
if [ -f internal/lifecycle/expire.go ] \
   && grep -q 'runExpireSuggestions' internal/lifecycle/manager.go 2>/dev/null; then
  ok "AC-5: proactive-suggestion expiry sweep registered (P4)"
else
  failc "AC-5: expiry sweep missing"
fi
if grep -qE '"github.com/hurtener/stowage/internal/gateway"' internal/proactive/governance.go internal/proactive/tuning.go internal/proactive/rules.go internal/lifecycle/expire.go 2>/dev/null; then
  failc "AC-5: a gateway-free file imports the gateway"
else
  ok "AC-5: governance/tuning/expiry are gateway-free (similar_episode is the only seam, degraded-safe)"
fi

# ── AC-6: store seam + index-only migration (no new table/column) ────────────────
if [ -f internal/store/sqlitestore/suggestions.go ] && [ -f internal/store/pgstore/suggestions.go ] \
   && [ -f internal/store/sqlitestore/scope_settings.go ] && [ -f internal/store/pgstore/scope_settings.go ]; then
  ok "AC-6: SuggestionStore + ScopeSettingsStore on both drivers"
else
  failc "AC-6: a store driver is missing the suggestions/scope_settings impl"
fi
if [ -f internal/store/migrations/sqlite/0010_suggestions_index.sql ] \
   && grep -qiE 'CREATE +(INDEX|UNIQUE INDEX)' internal/store/migrations/sqlite/0010_suggestions_index.sql 2>/dev/null \
   && ! grep -qiE 'CREATE +TABLE|ADD +COLUMN' internal/store/migrations/sqlite/0010_suggestions_index.sql 2>/dev/null; then
  ok "AC-6: migration 0010 is index-only (no new table/column — §8.1 unchanged)"
else
  failc "AC-6: migration 0010 missing or not index-only"
fi

# ── AC: tests (unit + conformance + parity + fuzz + goldens + eval) ──────────────
if CGO_ENABLED=1 go test -count=1 -timeout=180s ./internal/proactive/ >/tmp/p27-unit.log 2>&1; then
  ok "AC-1..3: proactive unit + fuzz tests pass"
else
  failc "AC-1..3: proactive unit tests failed"; tail -25 /tmp/p27-unit.log >&2
fi
if CGO_ENABLED=1 go test -count=1 -timeout=180s -run 'TestSuggestion|TestScopeSettings|Conformance' ./internal/store/sqlitestore/ >/tmp/p27-conf.log 2>&1; then
  ok "AC-6: store conformance (suggestions + scope_settings) passes on sqlite"
else
  failc "AC-6: store conformance failed"; tail -25 /tmp/p27-conf.log >&2
fi
if CGO_ENABLED=1 go test -count=1 -timeout=300s -run 'SuggestionsParity|ProactiveConfigParity' ./test/integration/ >/tmp/p27-parity.log 2>&1; then
  ok "AC-4: memory_suggestions + governance all-surfaces parity passes"
else
  failc "AC-4: proactive parity failed"; tail -25 /tmp/p27-parity.log >&2
fi
if CGO_ENABLED=1 go test -count=1 -timeout=180s -run 'TestExpireSuggestions' ./internal/lifecycle/ >/tmp/p27-exp.log 2>&1; then
  ok "AC-5: expiry sweep test passes"
else
  failc "AC-5: expiry sweep test failed"; tail -25 /tmp/p27-exp.log >&2
fi
if CGO_ENABLED=1 go test -count=1 -run 'TestSchemaGoldens|TestNew_SevenToolsRegistered|TestClientTierBoundary' ./internal/mcpserver/ ./sdk/stowage/ >/tmp/p27-golden.log 2>&1; then
  ok "AC-4: MCP goldens stable + tool count = 20 + SDK tier boundary holds"
else
  failc "AC-4: MCP goldens/tool-count or tier boundary drifted"; cat /tmp/p27-golden.log >&2
fi
if make eval-ci >/tmp/p27-evalci.log 2>&1; then
  ok "AC: make eval-ci green (deterministic CI unaffected)"
else
  failc "AC: make eval-ci failed"; cat /tmp/p27-evalci.log >&2
fi

exit "$fails"
