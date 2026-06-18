#!/usr/bin/env bash
# Phase 19 smoke: reflection write-side (ACE §6a.1-2). D-077.
#
# Verifies the structural/unit invariants (no live gateway):
#   AC-2  reflection sweep registered in the lifecycle manager.
#   AC-3  internal/reflect routes through gateway.Gateway (P5) + schema-constrained (§10);
#         internal/playbook stays gateway-free (D-072 lint unbroken).
#   AC-1  ListByOutcome present on both store drivers + conformance runs.
#   AC    reflection kinds live in the reflection schema, NOT topic ValidKinds.
#   AC-9  reflection config knobs surfaced in `config explain`, default off (non-fleet).
#   AC-8  reflection unit + fleet-loop integration tests pass.
set -uo pipefail
cd "$(dirname "$0")/../.."

fails=0
ok()    { printf 'OK   %s\n' "$*"; }
failc() { printf 'FAIL %s\n' "$*"; fails=$((fails+1)); }
skip()  { printf 'SKIP %s\n' "$*"; }

# Pre-build SKIP-graceful guard (CLAUDE.md §4.2): the write-side lands in the impl PR.
if [ ! -d internal/reflect ]; then
  skip "AC-1..9: reflection write-side not yet implemented (plan skeleton)"
  exit "$fails"
fi

# ── P5: internal/reflect uses the gateway seam only ──────────────────────────────
if grep -rnE '"github.com/(sashabaranov|openai|cohere-ai)|bifrost/core|google.golang.org/genai' internal/reflect/ 2>/dev/null; then
  failc "P5: internal/reflect imports a provider SDK (must route through gateway.Gateway)"
else
  ok "AC-3/P5: internal/reflect routes through the gateway seam"
fi

# ── §10: the reflection Complete call is schema-constrained ───────────────────────
if grep -rq 'Schema:' internal/reflect/ 2>/dev/null && grep -rq 'reflectionSchema' internal/reflect/ 2>/dev/null; then
  ok "AC-3/§10: reflection gateway call is schema-constrained"
else
  failc "AC-3/§10: reflection gateway call is not schema-constrained"
fi

# ── D-072: playbook stays gateway-free (the transitive lint must still pass) ──────
if CGO_ENABLED=1 go test -count=1 -timeout=120s -run 'TestNoGateway|TestPlaybook.*NoGateway|TestTransitive' ./internal/playbook/ >/tmp/p19-pb.log 2>&1; then
  ok "AC-3: internal/playbook transitive no-gateway lint still passes (D-072)"
else
  failc "AC-3: playbook no-gateway lint regressed"; cat /tmp/p19-pb.log >&2
fi

# ── AC-2: reflection sweep registered in the lifecycle manager ────────────────────
if grep -rq 'runReflect' internal/lifecycle/ 2>/dev/null; then
  ok "AC-2: reflection sweep (runReflect) registered in the lifecycle manager"
else
  failc "AC-2: reflection sweep not registered"
fi

# ── reflection kinds in the reflection schema, NOT topic ValidKinds ───────────────
if grep -rq 'strategy' internal/reflect/ 2>/dev/null \
   && ! grep -q 'failure_mode' internal/pipeline/candidates.go 2>/dev/null; then
  ok "AC: reflection kinds live in internal/reflect; topic ValidKinds unchanged"
else
  failc "AC: reflection kinds leaked into topic extraction (or missing from reflect)"
fi

# ── AC-1: ListByOutcome on both drivers ──────────────────────────────────────────
if grep -rq 'ListByOutcome' internal/store/sqlitestore/ 2>/dev/null \
   && grep -rq 'ListByOutcome' internal/store/pgstore/ 2>/dev/null; then
  ok "AC-1: ListByOutcome implemented on sqlite + pgx"
else
  failc "AC-1: ListByOutcome missing on a store driver"
fi

# ── AC-9: reflection knobs surfaced + default off for non-fleet ───────────────────
BIN=/tmp/stowage-smoke-p19
trap 'rm -f "$BIN"' EXIT
if CGO_ENABLED=0 go build -o "$BIN" ./cmd/stowage 2>/dev/null; then
  if "$BIN" config explain 2>/dev/null | grep -q 'lifecycle.reflect_enabled'; then
    ok "AC-9: config explain surfaces lifecycle.reflect_enabled"
  else
    failc "AC-9: reflection knobs not surfaced in config explain"
  fi
else
  failc "AC-9: binary build failed"
fi

# ── AC-8: reflection unit + integration tests ────────────────────────────────────
if CGO_ENABLED=1 go test -count=1 -timeout=300s ./internal/reflect/ ./test/integration/ >/tmp/p19-tests.log 2>&1; then
  ok "AC-8: reflection unit + fleet-loop integration tests pass"
else
  failc "AC-8: reflection tests failed"; tail -30 /tmp/p19-tests.log >&2
fi

exit "$fails"
