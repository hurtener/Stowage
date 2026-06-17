#!/usr/bin/env bash
# Phase h7 smoke: bifrost auto-wired Cohere-shape rerank over OpenRouter + bench
# rebase — D-075. (Live rerank is the `-tags=live` test, not this smoke.)
#
# AC verified when implemented:
#   AC-2  embed/complete route to the primary provider; rerank to the custom one
#   AC-6  gateway.rerank_base_url validates + appears in `config explain`
#
# SKELETON: SKIP until the rerank-provider wiring + knob exist (CLAUDE.md §4.2).
set -uo pipefail
cd "$(dirname "$0")/../.."
fails=0
ok(){ printf 'OK   %s\n' "$*"; }; failc(){ printf 'FAIL %s\n' "$*"; fails=$((fails+1)); }; skip(){ printf 'SKIP %s\n' "$*"; }

if ! grep -rq "rerank_base_url\|rerankProvider\|stowage-rerank" internal/gateway/bifrost/ internal/config/ 2>/dev/null; then
  skip "AC-2: bifrost custom rerank provider not yet wired (plan skeleton)"
  skip "AC-6: gateway.rerank_base_url knob (pending h7)"
  exit "$fails"
fi
skip "AC-2: rerank provider routing — wired by the h7 PR"
skip "AC-6: rerank_base_url knob + config explain — wired by the h7 PR"
exit "$fails"
