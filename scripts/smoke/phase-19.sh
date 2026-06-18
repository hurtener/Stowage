#!/usr/bin/env bash
# Phase 19 smoke: reflection write-side (ACE §6a.1-2). D-077.
#
# Verifies the structural/unit invariants (no live gateway):
#   AC-2  reflection sweep registered in the lifecycle manager.
#   AC-3  internal/reflect routes through gateway.Gateway (P5) + schema-constrained (§10);
#         internal/playbook stays gateway-free (D-072 lint unbroken).
#   AC-1  ListByOutcome present on both store drivers + conformance runs.
#   AC    reflection kinds live in the reflection schema, NOT the topic schema enum.
#   AC-9  reflection is profile-gated (ReflectConfigForProfile): fleet on, others off
#         — a profile-internal tuning like BufferTriggers/PlaybookBudget (D-077), not
#         a top-level config-explain knob.
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

# ── reflection kinds in the reflection schema, NOT the topic extraction schema ────
# (candidates.go legitimately names the kinds in ReflectionKinds/IsReflectionKind;
#  the invariant is that the topic-extraction CandidateSchema ENUM excludes them.)
topic_enum=$(grep '"enum"' internal/pipeline/candidates.go 2>/dev/null || true)
if printf '%s' "$topic_enum" | grep -qE 'strategy|failure_mode'; then
  failc "AC: reflection kinds leaked into the topic-extraction schema enum"
elif grep -q 'strategy' internal/reflect/schema.go 2>/dev/null && grep -q 'failure_mode' internal/reflect/schema.go 2>/dev/null; then
  ok "AC: reflection kinds in the reflection schema; topic schema enum excludes them"
else
  failc "AC: reflection schema missing the reflection kinds"
fi

# ── AC-1: ListByOutcome on both drivers ──────────────────────────────────────────
if grep -rq 'ListByOutcome' internal/store/sqlitestore/ 2>/dev/null \
   && grep -rq 'ListByOutcome' internal/store/pgstore/ 2>/dev/null; then
  ok "AC-1: ListByOutcome implemented on sqlite + pgx"
else
  failc "AC-1: ListByOutcome missing on a store driver"
fi

# ── AC-9: reflection is profile-gated (fleet on, single-user off) ─────────────────
# Profile-internal tuning (like BufferTriggers/PlaybookBudget), proven by the
# config unit test rather than config explain (D-077).
if grep -q 'func ReflectConfigForProfile' internal/config/profiles.go 2>/dev/null \
   && CGO_ENABLED=1 go test -count=1 -timeout=60s -run 'TestReflectConfigForProfile' ./internal/config/ >/tmp/p19-cfg.log 2>&1; then
  ok "AC-9: reflection profile-gated (fleet on, assistant/coding-agent off)"
else
  failc "AC-9: ReflectConfigForProfile gating test failed"; cat /tmp/p19-cfg.log >&2
fi
# Zero-config single-user start does no reflection (binary builds, default profile off).
BIN=/tmp/stowage-smoke-p19
trap 'rm -f "$BIN"' EXIT
if CGO_ENABLED=0 go build -o "$BIN" ./cmd/stowage 2>/dev/null; then
  ok "AC-9: CGo-free binary builds with reflection wired"
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
