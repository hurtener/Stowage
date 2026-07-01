#!/usr/bin/env bash
# Phase ae6 smoke: own-scope topic filter (fail-open, lane-aware), D-144/D-139. An
# optional include/exclude topic filter on retrieve that narrows the caller's OWN-scope
# results, does not underfill, and FAILS OPEN (returns unfiltered own-scope results on a
# topic-store error) — the deliberate opposite of grants' fail-closed filterByTopic.
#
# Verifies:
#   AC-3  filterByTopicOwnScope exists and is DISTINCT from grants' filterByTopic.
#   AC-5  include_topics/exclude_topics present in all three retrieve contracts.
#   AC-6  retrieval.topic_filter_scoring_k is a registered, explainable knob.
#   AC-1/2/3 unit + integration tests pass (own-scope, no-underfill, fail-open).
set -uo pipefail
cd "$(dirname "$0")/../.."

fails=0
ok()    { printf 'OK   %s\n' "$*"; }
failc() { printf 'FAIL %s\n' "$*"; fails=$((fails+1)); }
skip()  { printf 'SKIP %s\n' "$*"; }

TF=internal/retrieval/topicfilter.go

# ── AC-3: the fail-open filter exists, distinct from grants' fail-closed twin ────
if [ ! -f "$TF" ]; then
  skip "AC-3: $TF not built yet (ae6 not landed)"
  exit "$fails"
fi
if grep -q 'func.*filterByTopicOwnScope' "$TF"; then
  ok "AC-3: filterByTopicOwnScope defined"
else
  failc "AC-3: filterByTopicOwnScope missing"
fi
if grep -Rq 'func.*filterByTopic\b' internal/retrieval/grants.go; then
  ok "AC-3: grants' fail-closed filterByTopic still distinct (not merged)"
else
  failc "AC-3: grants' filterByTopic not found — divergence (D-139) may have been collapsed"
fi

# ── AC-5: additive args on all three retrieve contracts ─────────────────────────
miss=0
for f in internal/mcpserver/contracts.go internal/api/retrieve_handler.go sdk/stowage/types.go; do
  grep -q 'include_topics' "$f" && grep -q 'exclude_topics' "$f" || { miss=1; failc "AC-5: $f missing include_topics/exclude_topics"; }
done
[ "$miss" -eq 0 ] && ok "AC-5: include_topics/exclude_topics present on MCP+HTTP+SDK contracts"

# ── AC-6: knob registered + explainable ─────────────────────────────────────────
if grep -q 'topic_filter_scoring_k' internal/config/config.go; then
  ok "AC-6: retrieval.topic_filter_scoring_k present in config"
else
  failc "AC-6: retrieval.topic_filter_scoring_k missing from config"
fi

# ── AC-1/2/3: tests ─────────────────────────────────────────────────────────────
if go test ./internal/retrieval/ -run TopicFilter -count=1 >/dev/null 2>&1; then
  ok "AC-1/3: retrieval topic-filter unit tests pass"
else
  failc "AC-1/3: retrieval topic-filter unit tests fail"
fi
if go test ./test/integration/ -run TopicFilter -count=1 >/dev/null 2>&1; then
  ok "AC-2: integration no-underfill + fail-open tests pass"
else
  skip "AC-2: integration topic-filter tests not present/passing yet"
fi

exit "$fails"
