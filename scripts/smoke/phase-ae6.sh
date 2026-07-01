#!/usr/bin/env bash
# Phase ae6 smoke: own-scope topic filter (fail-open, lane-aware), D-144/D-139. An
# optional include/exclude topic filter on retrieve that narrows the caller's OWN-scope
# results, does not underfill, and FAILS OPEN (returns unfiltered own-scope results on a
# topic-store error) — the deliberate opposite of grants' fail-closed filterByTopic.
#
# Verifies:
#   AC-3   filterByTopicOwnScope exists and is DISTINCT from grants' filterByTopic
#          (opposite error-branch shapes: fail-OPEN vs fail-CLOSED).
#   AC-5   include_topics/exclude_topics + DegradedTopicFilter present on all three
#          retrieve contracts (MCP/HTTP/SDK), and the MCP schema goldens carry them.
#   AC-6   retrieval.topic_filter_scoring_k is a registered, explainable knob (D-034).
#   AC-7   the filter is gateway-free (D-036) — topicfilter.go imports no gateway package.
#   AC-1/2/3 unit + MCP schema + integration tests pass (own-scope, no-underfill, fail-open).
set -uo pipefail
cd "$(dirname "$0")/../.."

fails=0
ok()    { printf 'OK   %s\n' "$*"; }
failc() { printf 'FAIL %s\n' "$*"; fails=$((fails+1)); }
skip()  { printf 'SKIP %s\n' "$*"; }

TF=internal/retrieval/topicfilter.go
GRANTS=internal/retrieval/grants.go
RETRIEVAL=internal/retrieval/retrieval.go
CFG=internal/config/config.go
SCHEMA_IN=internal/mcpserver/testdata/memory_retrieve.input.schema.json
SCHEMA_OUT=internal/mcpserver/testdata/memory_retrieve.output.schema.json
PARITY=test/integration/retrieve_topicfilter_test.go

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
if grep -Rq 'func.*filterByTopic\b' "$GRANTS"; then
  ok "AC-3: grants' fail-closed filterByTopic still distinct (not merged)"
else
  failc "AC-3: grants' filterByTopic not found — divergence (D-139) may have been collapsed"
fi
if grep -q 'return ids, true' "$TF"; then
  ok "AC-3: filterByTopicOwnScope's error branch returns the input unchanged (fail-OPEN)"
else
  failc "AC-3: fail-open return shape not found in $TF"
fi
if grep -q 'return nil' "$GRANTS"; then
  ok "AC-3: grants' filterByTopic error branch returns nil (fail-CLOSED, contrast preserved)"
else
  failc "AC-3: grants' fail-closed return shape not found in $GRANTS"
fi

# ── AC-7 (D-036): the filter is a pure store read — no gateway import ───────────
if grep -q 'internal/gateway' "$TF"; then
  failc "AC-7: topicfilter.go imports the gateway (must be gateway-free, D-036)"
else
  ok "AC-7: topicfilter.go is gateway-free (D-036)"
fi

# ── D-144: pre-trim placement + widening knob wired into Retrieve ───────────────
if grep -q 'filterByTopicOwnScope' "$RETRIEVAL"; then
  ok "D-144: Retrieve calls filterByTopicOwnScope"
else
  failc "D-144: Retrieve does not call filterByTopicOwnScope"
fi
if grep -q 'topicFilterScoringK' "$RETRIEVAL"; then
  ok "D-144: scoringK widening wired for an active topic filter"
else
  failc "D-144: no scoringK widening reference in retrieval.go"
fi

# ── AC-5: additive args + DegradedTopicFilter on all three retrieve contracts ───
miss=0
for f in internal/mcpserver/contracts.go internal/api/retrieve_handler.go sdk/stowage/types.go; do
  grep -q 'IncludeTopics\|include_topics' "$f" && grep -q 'ExcludeTopics\|exclude_topics' "$f" || { miss=1; failc "AC-5: $f missing include_topics/exclude_topics"; }
done
[ "$miss" -eq 0 ] && ok "AC-5: include_topics/exclude_topics present on MCP+HTTP+SDK contracts"

miss=0
for f in "$RETRIEVAL" internal/mcpserver/contracts.go internal/api/retrieve_handler.go sdk/stowage/types.go; do
  grep -q 'DegradedTopicFilter' "$f" || { miss=1; failc "AC-5: $f missing DegradedTopicFilter"; }
done
[ "$miss" -eq 0 ] && ok "AC-5: DegradedTopicFilter present on retrieval.Response + all three output types"

if grep -q 'IncludeTopics' internal/mcpserver/handlers.go && grep -q 'IncludeTopics' sdk/stowage/embedded.go; then
  ok "AC-5: MCP + embedded SDK call sites thread IncludeTopics/ExcludeTopics into retrieval.Request"
else
  failc "AC-5: a call site does not thread IncludeTopics/ExcludeTopics into retrieval.Request"
fi

if grep -q 'include_topics' "$SCHEMA_IN" 2>/dev/null && grep -q 'exclude_topics' "$SCHEMA_IN" 2>/dev/null \
   && grep -q 'degraded_topic_filter' "$SCHEMA_OUT" 2>/dev/null; then
  ok "AC-5: MCP schema goldens carry include_topics/exclude_topics/degraded_topic_filter"
else
  failc "AC-5: MCP schema goldens missing the new fields — run UPDATE_GOLDEN=1 go test ./internal/mcpserver/ -run TestSchemaGoldens"
fi

# ── AC-6: knob registered + explainable (D-034) ─────────────────────────────────
if grep -q 'topic_filter_scoring_k' "$CFG"; then
  ok "AC-6: retrieval.topic_filter_scoring_k present in config"
  BIN=/tmp/stowage-smoke-ae6
  trap 'rm -f "$BIN"' EXIT
  if CGO_ENABLED=0 go build -o "$BIN" ./cmd/stowage 2>/dev/null; then
    EXPLAIN_OUT=$("$BIN" config explain 2>&1 || true)
    if echo "$EXPLAIN_OUT" | grep -Eq 'retrieval\.topic_filter_scoring_k[[:space:]]+=[[:space:]]+100[[:space:]]+\[default\]'; then
      ok "AC-6: config explain shows retrieval.topic_filter_scoring_k = 100 [default]"
    else
      failc "AC-6: topic_filter_scoring_k default not found via config explain (got: $(echo "$EXPLAIN_OUT" | grep topic_filter_scoring_k || echo '(nothing)'))"
    fi
  else
    failc "AC-6: cgo-free build failed — cannot check config explain"
  fi
else
  failc "AC-6: retrieval.topic_filter_scoring_k missing from config"
fi

# ── AC-1/2/3: tests ──────────────────────────────────────────────────────────────
if go test ./internal/retrieval/ -run TopicFilter -count=1 >/dev/null 2>&1; then
  ok "AC-1/3: retrieval topic-filter unit tests pass"
else
  failc "AC-1/3: retrieval topic-filter unit tests fail"
fi
if go test ./internal/mcpserver/ -run TestSchemaGoldens/memory_retrieve -count=1 >/dev/null 2>&1; then
  ok "AC-5: MCP memory_retrieve schema golden test passes"
else
  failc "AC-5: MCP memory_retrieve schema golden test fails"
fi
if [ -f "$PARITY" ]; then
  if go test ./test/integration/ -run TopicFilter -count=1 >/dev/null 2>&1; then
    ok "AC-2: integration no-underfill + fail-open + scope-isolation tests pass"
  else
    failc "AC-2: integration topic-filter tests fail"
  fi
else
  skip "AC-2: $PARITY not present yet"
fi

exit "$fails"
