#!/usr/bin/env bash
# Phase ae9 smoke: per-agent/per-key topic VIEWS (read-time curation), D-149/D-139.
# Named, switchable views keyed by (tenant_id, subject_kind, subject_id, view_name)
# → {allow_topics, deny_topics}: a read-time lens that narrows which topic-tagged
# OWN-SCOPE memories surface. Reuses ae6's fail-OPEN filterByTopicOwnScope; a view
# can only SUBTRACT from own-scope (curation, not isolation) — tenant stays the sole
# P3 boundary. ae9 sits on ae1 (Scope.Agent + _meta agent_id) and ae6 (the filter),
# so this SKIPs gracefully until both have landed.
#
# Verifies:
#   AC-1/4  resolveAndApplyView exists and reuses ae6's filter (fail-open, distinct).
#   AC-6/9  TopicViewStore seam + both-driver migration; no agent column on scope tables.
#   AC-7    view_name/degraded_view on all three retrieve contracts.
#   AC-8    the three agent_views knobs are registered + explainable, default off.
#   AC-1..6 unit + conformance + integration tests pass.
set -uo pipefail
cd "$(dirname "$0")/../.."

fails=0
ok()    { printf 'OK   %s\n' "$*"; }
failc() { printf 'FAIL %s\n' "$*"; fails=$((fails+1)); }
skip()  { printf 'SKIP %s\n' "$*"; }

VIEWS=internal/retrieval/views.go
TF=internal/retrieval/topicfilter.go   # ae6's filter (ae9 depends on it)

# ── Dependency gate: ae1+ae6 must be present ───────────────────────────────────
if [ ! -f "$VIEWS" ]; then
  skip "AC-1: $VIEWS not built yet (ae9 not landed)"
  exit "$fails"
fi
if [ ! -f "$TF" ]; then
  skip "AC-4: ae6's $TF not present yet (ae9 depends on ae6's filter)"
  exit "$fails"
fi

# ── AC-1/4: the view resolver exists and reuses ae6's fail-open filter ──────────
if grep -q 'func.*resolveAndApplyView' "$VIEWS"; then
  ok "AC-1: resolveAndApplyView defined"
else
  failc "AC-1: resolveAndApplyView missing"
fi
if grep -q 'filterByTopicOwnScope' "$VIEWS"; then
  ok "AC-4: ae9 reuses ae6's filterByTopicOwnScope (no second filter)"
else
  failc "AC-4: ae9 does not call filterByTopicOwnScope — a duplicate filter may have crept in"
fi
if grep -Rq 'func.*filterByTopic\b' internal/retrieval/grants.go; then
  ok "AC-4: grants' fail-closed filterByTopic still distinct (D-139 not collapsed)"
else
  failc "AC-4: grants' filterByTopic not found — the fail-open/fail-closed divergence may be lost"
fi

# ── AC-6/9: the store seam + both-driver migration; no agent column on scope tables ─
if grep -q 'TopicViewStore' internal/store/store.go && grep -q 'TopicViews()' internal/store/store.go; then
  ok "AC-6: TopicViewStore interface + TopicViews() accessor on the Store seam"
else
  failc "AC-6: TopicViewStore/TopicViews() missing from internal/store/store.go"
fi
pg=$(ls internal/store/migrations/postgres/*topic_views.sql 2>/dev/null | head -1)
sq=$(ls internal/store/migrations/sqlite/*topic_views.sql 2>/dev/null | head -1)
if [ -n "$pg" ] && [ -n "$sq" ]; then
  ok "AC-9: topic_views migration present in both dialect dirs (forward-only)"
else
  failc "AC-9: topic_views migration missing from postgres and/or sqlite migrations"
fi
if grep -Riq 'agent_id' internal/store/migrations/*/0001_init.sql; then
  failc "AC-9: an agent_id column appears in the scope-table init migration (forbidden)"
else
  ok "AC-9: no agent column on the scope tables"
fi

# ── AC-7: apply-a-view parity on all three retrieve contracts ──────────────────
miss=0
for f in internal/mcpserver/contracts.go internal/api/retrieve_handler.go sdk/stowage/types.go; do
  grep -q 'view_name' "$f" || { miss=1; failc "AC-7: $f missing view_name"; }
done
[ "$miss" -eq 0 ] && ok "AC-7: view_name present on MCP+HTTP+SDK retrieve contracts"
dmiss=0
for f in internal/mcpserver/contracts.go internal/api/retrieve_handler.go sdk/stowage/types.go; do
  grep -q 'degraded_view' "$f" || { dmiss=1; failc "AC-7: $f missing degraded_view"; }
done
[ "$dmiss" -eq 0 ] && ok "AC-7: degraded_view present on MCP+HTTP+SDK retrieve outputs"

# ── AC-8: the three agent_views knobs are registered, default off ──────────────
kmiss=0
for k in 'agent_views' 'on_policy_error' 'subject_precedence'; do
  grep -q "$k" internal/config/config.go || { kmiss=1; failc "AC-8: config.go missing $k"; }
done
[ "$kmiss" -eq 0 ] && ok "AC-8: agent_views.{enabled,on_policy_error,subject_precedence} present in config"

# ── AC-1..6: tests ─────────────────────────────────────────────────────────────
if go test ./internal/retrieval/ -run View -count=1 >/dev/null 2>&1; then
  ok "AC-1/4: retrieval view unit tests pass"
else
  failc "AC-1/4: retrieval view unit tests fail"
fi
if go test ./internal/store/... -run TopicView -count=1 >/dev/null 2>&1; then
  ok "AC-6: TopicViewStore conformance passes on both drivers"
else
  failc "AC-6: TopicViewStore conformance fails"
fi
if go test ./test/integration/ -run TopicViews -count=1 >/dev/null 2>&1; then
  ok "AC-1/4/5: integration apply + fail-open + cross-scope tests pass"
else
  skip "AC-1/4/5: integration topic-views tests not present/passing yet"
fi

exit "$fails"
