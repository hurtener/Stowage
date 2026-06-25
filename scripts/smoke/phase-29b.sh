#!/usr/bin/env bash
# Smoke for Phase 29b — context-aware reconciliation (D-108): the supersede/merge decision
# sees the candidate's + neighbors' original conversation turns. Behaviour change, not
# surface. Exit non-zero iff any FAIL.
set -uo pipefail
cd "$(dirname "$0")/../.."
fails=0
ok()   { echo "OK   $*"; }
failc(){ echo "FAIL $*"; fails=$((fails+1)); }

if grep -q "Original conversation context" internal/reconcile/prompt.go; then
  ok "decision prompt renders the conversation-context section"
else
  failc "conversation-context section missing from prompt"
fi
if grep -q "DIFFERENT fact that merely shares words" internal/reconcile/prompt.go; then
  ok "system prompt carries correction-vs-distinct-fact rule"
else
  failc "system prompt missing the D-108 disambiguation rule"
fi
if grep -q "SetRecordStore(stk.Store.Records())" internal/boot/pipeline.go; then
  ok "boot wires RecordStore into the reconcile stage"
else
  failc "boot does not wire RecordStore into reconcile"
fi
if go test ./internal/reconcile/ -run 'TestBuildUserPrompt_ConversationContext' -count=1 >/dev/null 2>&1; then
  ok "context-aware prompt unit test passes"
else
  failc "context-aware prompt unit test"
fi
exit "$fails"
