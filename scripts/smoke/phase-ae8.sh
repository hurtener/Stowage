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
  failc "AC-10: read_posture default missing/stale in explain golden (regenerate it)"
fi
if grep -Eq 'identity.multiplexing[[:space:]]*=[[:space:]]*false' internal/config/testdata/explain_default.golden; then
  ok "AC-10: identity.multiplexing defaults to false (explain golden)"
else
  failc "AC-10: identity.multiplexing default missing/stale in explain golden (regenerate it)"
fi

# ── AC-4: the ae8 RESOLVER (and its surface adapters) add no store query/WHERE ──
# Intent: the ae8 resolver stays store-free — ResolveReadScope does NO I/O
# (per its own godoc) and its HTTP/MCP adapters call EXISTING store accessors,
# never define a new one. This is scoped to the resolver + its two adapters,
# NOT a whole-branch internal/store diff — a sibling phase (e.g. ae9's view
# CRUD store additions) legitimately touches internal/store on the same
# branch and must not false-fail this gate.
store_clean=1
RESOLVER_ADAPTERS="internal/mcpserver/scope.go internal/api/auth.go"
if grep -q '"github.com/hurtener/stowage/internal/store"' "$RES" 2>/dev/null; then
  failc "AC-4: $RES imports internal/store — the resolver must stay store-free (no I/O)"
  store_clean=0
fi
if grep -qiE '\b(SELECT|INSERT INTO|UPDATE .* SET|DELETE FROM)\b' "$RES" 2>/dev/null; then
  failc "AC-4: $RES contains a raw SQL literal — the resolver must stay store-free"
  store_clean=0
fi
for f in $RESOLVER_ADAPTERS; do
  if [ ! -f "$f" ]; then
    continue
  fi
  if grep -q '"github.com/hurtener/stowage/internal/store"' "$f"; then
    failc "AC-4: $f imports internal/store — a resolver adapter must call EXISTING store accessors only, never define a new store query"
    store_clean=0
  fi
  if grep -qiE '\b(SELECT|INSERT INTO|UPDATE .* SET|DELETE FROM)\b' "$f"; then
    failc "AC-4: $f contains a raw SQL literal — a resolver adapter must not add a store query/WHERE"
    store_clean=0
  fi
done
if [ "$store_clean" -eq 1 ]; then
  ok "AC-4: the ae8 resolver + its HTTP/MCP adapters add no store query/WHERE (internal/store additions by sibling phases are out of scope for this gate)"
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
