#!/usr/bin/env bash
# Phase 20b smoke: gain harness (memory on vs off) + online-adaptation. D-078.
#
# Deterministic, no-key checks only (the real gain/adaptation numbers are
# operator-run, full-mode):
#   AC-1  the reader+judge is reused + schema-constrained (§10); gain logic present.
#   AC-2  no provider SDK under eval/ (P5).
#   AC-3  gain pure-logic + fakeGateway on/off delta tests pass.
#   AC-4  online-adaptation loop wiring test passes (reflection→playbook→judge).
#   AC-6  full-mode build compiles (-tags=fullmode); eval-ci still green.
set -uo pipefail
cd "$(dirname "$0")/../.."

fails=0
ok()    { printf 'OK   %s\n' "$*"; }
failc() { printf 'FAIL %s\n' "$*"; fails=$((fails+1)); }
skip()  { printf 'SKIP %s\n' "$*"; }

if [ ! -f eval/harness/gain.go ]; then
  skip "AC-1..6: gain harness not yet implemented (plan skeleton)"
  exit "$fails"
fi

# ── AC-2: no provider SDK under eval/ (P5) ───────────────────────────────────────
if grep -rnE '"github.com/(sashabaranov|openai|cohere-ai)|bifrost/core|google.golang.org/genai' eval/ 2>/dev/null; then
  failc "AC-2: a provider SDK is imported under eval/ (must route through gateway.Gateway)"
else
  ok "AC-2/P5: no provider SDK under eval/"
fi

# ── AC-1: gain reuses the schema-constrained reader+judge ────────────────────────
if grep -q 'JudgeQuestion' eval/harness/gain.go 2>/dev/null && grep -q 'func RunGainScenario' eval/harness/gain.go 2>/dev/null; then
  ok "AC-1: gain harness reuses the Phase-20 reader+judge"
else
  failc "AC-1: gain harness does not reuse JudgeQuestion"
fi

# ── AC-4: online-adaptation wires reflection + playbook ──────────────────────────
if grep -q 'reflect.Reflect' eval/harness/adapt.go 2>/dev/null && grep -q 'playbook.Assemble' eval/harness/adapt.go 2>/dev/null; then
  ok "AC-4: online-adaptation wires the reflection→playbook loop"
else
  failc "AC-4: online-adaptation missing reflection/playbook wiring"
fi

# ── AC-3/AC-4: gain + adaptation unit tests ──────────────────────────────────────
if CGO_ENABLED=1 go test -count=1 -timeout=180s \
     -run 'TestGain|TestAggregate|TestScenarioToFixture|TestJudgeOnOff|TestRunAdaptScenario' \
     ./eval/harness/ >/tmp/p20b-unit.log 2>&1; then
  ok "AC-3/AC-4: gain pure-logic + on/off delta + adaptation loop tests pass"
else
  failc "AC-3/AC-4: gain/adaptation tests failed"; tail -25 /tmp/p20b-unit.log >&2
fi

# ── AC-6: full-mode build compiles ───────────────────────────────────────────────
if go vet -tags=fullmode ./eval/harness/ >/tmp/p20b-vet.log 2>&1; then
  ok "AC-6: full-mode build compiles (-tags=fullmode)"
else
  failc "AC-6: full-mode build does not compile"; cat /tmp/p20b-vet.log >&2
fi

# ── AC-6: CI eval gate unaffected ────────────────────────────────────────────────
if make eval-ci >/tmp/p20b-evalci.log 2>&1; then
  ok "AC-6: make eval-ci green (deterministic CI metric unaffected)"
else
  failc "AC-6: make eval-ci failed"; cat /tmp/p20b-evalci.log >&2
fi

exit "$fails"
