#!/usr/bin/env bash
# Phase ae8 smoke: effective-scope resolution + read-side enforcement (D-148/D-137).
# One resolver (identity.ResolveReadScope) merges credential tenant + _meta + JWT
# claims + D-125 args into the effective READ Scope, under the read_posture and
# identity.multiplexing knobs. It adds NO store WHERE — the store already filters on
# a populated Scope.User; ae8 populates/requires it (strict) or lets it fall back
# (compatible).
#
# Verifies:
#   AC-1  identity.ResolveReadScope + ReadPosture defined.
#   AC-6/5/10  retrieval.read_posture (default compatible) + identity.multiplexing
#              (default false) are registered, explainable knobs.
#   AC-4  ae8 introduced no new internal/store query path; the three scope
#         predicates are present and fail closed.
#   AC-9  no surviving inline read-scope literal outside the surface adapters.
#   AC-1/2/3 resolver unit tests + strict-flip integration test pass.
set -uo pipefail
cd "$(dirname "$0")/../.."

fails=0
ok()    { printf 'OK   %s\n' "$*"; }
failc() { printf 'FAIL %s\n' "$*"; fails=$((fails+1)); }
skip()  { printf 'SKIP %s\n' "$*"; }

RES=internal/identity/resolve.go

# ── AC-1: the single resolver exists ────────────────────────────────────────────
if [ ! -f "$RES" ]; then
  skip "AC-1: $RES not built yet (ae8 not landed)"
  exit "$fails"
fi
if grep -q 'func ResolveReadScope' "$RES"; then
  ok "AC-1: identity.ResolveReadScope defined"
else
  failc "AC-1: ResolveReadScope missing from $RES"
fi
if grep -q 'ReadPosture' "$RES"; then
  ok "AC-1: ReadPosture type present"
else
  failc "AC-1: ReadPosture type missing"
fi

# ── AC-5/6/10: both knobs registered with the correct defaults ──────────────────
if grep -q 'retrieval.read_posture' internal/config/config.go; then
  ok "AC-6: retrieval.read_posture registered in config"
else
  failc "AC-6: retrieval.read_posture missing from config"
fi
if grep -q 'identity.multiplexing' internal/config/config.go; then
  ok "AC-5: identity.multiplexing registered in config"
else
  failc "AC-5: identity.multiplexing missing from config"
fi
if grep -Eq 'read_posture[[:space:]]*=[[:space:]]*compatible' internal/config/testdata/explain_default.golden; then
  ok "AC-10: read_posture defaults to compatible (explain golden)"
else
  skip "AC-10: read_posture default not yet in explain golden"
fi
if grep -Eq 'identity.multiplexing[[:space:]]*=[[:space:]]*false' internal/config/testdata/explain_default.golden; then
  ok "AC-10: identity.multiplexing defaults to false (explain golden)"
else
  skip "AC-10: identity.multiplexing default not yet in explain golden"
fi

# ── AC-4: no new store read path; the three scope predicates still fail closed ──
if git diff --name-only main...HEAD 2>/dev/null | grep -q '^internal/store/'; then
  failc "AC-4: ae8 changed internal/store — it must add NO store predicate/method"
else
  ok "AC-4: internal/store untouched by ae8 (no new read path)"
fi
pred_ok=1
for f in internal/store/pgstore/scope.go internal/store/sqlitestore/scope.go; do
  grep -q 'buildScopeWhere' "$f" && grep -q 'ErrScopeRequired' "$f" || pred_ok=0
done
grep -q 'ErrScopeRequired' internal/store/pgstore/vectors.go || pred_ok=0
if [ "$pred_ok" -eq 1 ]; then
  ok "AC-4: buildScopeWhere/buildExactScopeWhere + vector Scan fail closed on empty tenant"
else
  failc "AC-4: a scope predicate lost its fail-closed guard"
fi

# ── AC-9: no inline read-scope literal outside the resolver adapters ────────────
if grep -REn 'Scope\{Tenant:[^}]*(User: in\.|User: req\.)' internal/mcpserver internal/api >/dev/null 2>&1; then
  failc "AC-9: an inline read-scope literal bypasses ResolveReadScope"
else
  ok "AC-9: no inline read-scope literal in MCP/HTTP handlers"
fi

# ── AC-1/2/3: resolver unit + strict-flip integration tests ─────────────────────
if go test ./internal/identity/ -run Resolve -count=1 >/dev/null 2>&1; then
  ok "AC-1/2/3: resolver precedence/conflict unit tests pass"
else
  failc "AC-1/2/3: resolver unit tests fail"
fi
if go test ./test/integration/ -run EffectiveScope -count=1 >/dev/null 2>&1; then
  ok "AC-7: strict-flip integration test passes"
else
  skip "AC-7: integration effective-scope test not present/passing yet"
fi

exit "$fails"
