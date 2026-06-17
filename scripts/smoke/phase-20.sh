#!/usr/bin/env bash
# Phase 20 smoke: eval finalization — judged QA (reader + LLM judge) + deterministic
# normalization + competitor report. D-076.
#
# The judged path is opt-in and full-mode only (needs a paid LLM); this smoke
# verifies the DETERMINISTIC, no-key pieces only:
#   AC-2  normalization (number-word + either-direction) is pure + table-tested.
#   AC-1  the CI eval gate is unaffected (deterministic, mock, string-match).
#   AC-3  the judged path is opt-in (gated on STOWAGE_EVAL_JUDGE).
#   AC-4  the reader+judge Complete calls are JSON-schema-constrained (§10). The
#         validated resp.JSON IS unmarshaled (the correct gateway-caller idiom —
#         not free-text parsing); the invariant is that a Schema is always set.
#   AC-5  no provider SDK is imported under eval/ (P5).
#   AC-7  the full-mode build compiles under -tags=fullmode.
#   AC-6  (if shipped) `stowage eval fetch --variant` is accepted / bad rejected.
set -uo pipefail
cd "$(dirname "$0")/../.."

fails=0
ok()    { printf 'OK   %s\n' "$*"; }
failc() { printf 'FAIL %s\n' "$*"; fails=$((fails+1)); }
skip()  { printf 'SKIP %s\n' "$*"; }

# Pre-build SKIP-graceful guard (CLAUDE.md §4.2): the judged path lands in PR #2.
if [ ! -f eval/harness/judge.go ]; then
  skip "AC-1..7: judged-QA path not yet implemented (plan skeleton — PR #2)"
  exit "$fails"
fi

# ── AC-5: no provider SDK imported under eval/ (P5) ─────────────────────────────
if grep -rnE '"github.com/(sashabaranov|openai)|bifrost/core|cohere-ai|google.golang.org/genai' eval/ 2>/dev/null; then
  failc "AC-5: a provider SDK is imported under eval/ (must route through gateway.Gateway)"
else
  ok "AC-5: no provider SDK under eval/ — reader/judge route through the gateway seam"
fi

# ── AC-4: reader+judge Complete calls are JSON-schema-constrained (§10) ─────────
# gateway.Complete REQUIRES a schema and returns validated JSON; unmarshaling that
# validated resp.JSON is the standard idiom (extract.go/reconcile.go do the same),
# NOT free-text parsing. The invariant we assert: both calls set a Schema.
schema_calls=$(grep -c 'Schema:' eval/harness/judge.go 2>/dev/null); schema_calls=${schema_calls:-0}
if grep -q 'readerSchema' eval/harness/judge.go 2>/dev/null \
   && grep -q 'judgeSchema' eval/harness/judge.go 2>/dev/null \
   && [ "$schema_calls" -ge 2 ]; then
  ok "AC-4: reader+judge Complete calls are schema-constrained (§10)"
else
  failc "AC-4: judge.go does not schema-constrain both Complete calls (Schema: x$schema_calls)"
fi

# ── AC-3: judged path is opt-in (gated on STOWAGE_EVAL_JUDGE) ────────────────────
if grep -q 'STOWAGE_EVAL_JUDGE' eval/harness/fullmode_test.go 2>/dev/null; then
  ok "AC-3: judged path gated on STOWAGE_EVAL_JUDGE (opt-in)"
else
  failc "AC-3: judged path is not gated on STOWAGE_EVAL_JUDGE"
fi

# ── AC-2: normalization unit tests + AC: judge prompt golden ────────────────────
if CGO_ENABLED=1 go test -count=1 -timeout=180s \
     -run 'TestNormalize|TestAnswerContextHit|TestJudgePrompt|TestReaderPrompt' \
     ./eval/harness/ >/tmp/phase20-unit.log 2>&1; then
  ok "AC-2/AC-4: normalization tables + reader/judge prompt goldens pass"
else
  failc "AC-2/AC-4: normalization or prompt-golden tests failed"
  cat /tmp/phase20-unit.log >&2
fi

# ── AC-7: full-mode build compiles under the fullmode tag ───────────────────────
if go vet -tags=fullmode ./eval/harness/ >/tmp/phase20-vet.log 2>&1; then
  ok "AC-7: full-mode build compiles (-tags=fullmode)"
else
  failc "AC-7: full-mode build does not compile"
  cat /tmp/phase20-vet.log >&2
fi

# ── AC-1: the CI eval gate is unaffected (deterministic, mock) ──────────────────
if make eval-ci >/tmp/phase20-evalci.log 2>&1; then
  ok "AC-1: make eval-ci green (deterministic CI metric unaffected)"
else
  failc "AC-1: make eval-ci failed"
  cat /tmp/phase20-evalci.log >&2
fi

# ── AC-6: optional `eval fetch --variant` flag (if shipped) ─────────────────────
if grep -q -- '--variant' cmd/stowage/main.go 2>/dev/null; then
  BIN=/tmp/stowage-smoke-p20
  trap 'rm -f "$BIN"' EXIT
  if CGO_ENABLED=0 go build -o "$BIN" ./cmd/stowage 2>/dev/null; then
    if "$BIN" eval fetch --variant bogus 2>&1 | grep -qiE 'variant|unknown|invalid'; then
      ok "AC-6: eval fetch rejects an unknown --variant"
    else
      failc "AC-6: eval fetch did not reject an unknown --variant"
    fi
  else
    failc "AC-6: binary build for --variant check failed"
  fi
else
  skip "AC-6: eval fetch --variant flag not shipped (env override used instead)"
fi

exit "$fails"
