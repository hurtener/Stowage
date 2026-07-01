#!/usr/bin/env bash
# Phase ae1 smoke: read-time agent identity dimension (+ Dockyard bump), D-135/D-146,
# generalized per D-151. An optional identity.Scope.Agent (read-path only) + a
# (tenant_id, agent_id)→topic policy binding — stored as (subject_kind='agent',
# view_name='default') rows in the general topic_views table (D-151) — feed ae6's
# own-scope fail-open filter to narrow a tenant's OWN retrieval by the calling agent —
# with ZERO scope-table schema change and ZERO write-path change. Agent read-filter on
# {SDK,HTTP,MCP}; policy admin on {HTTP,MCP}.
#
# Verifies:
#   AC-1  Scope.Agent exists and is inert in both drivers' scope-WHERE builders.
#   AC-2  No agent column / no scope-table migration; source_agent stays records-only.
#   AC-3  go.mod pins dockyard v1.8.0; handler reads server.RequestMeta (no MetaFromContext).
#   AC-4  TopicViewStore seam + 0013 migration on both drivers.
#   AC-7  ae1 resolves agent topics but reuses ae6's filterByTopicOwnScope (no 2nd filter).
#   AC-8  agent_id intake on HTTP+SDK retrieve contracts.
#   AC-9  memory_agent_policy tool + HTTP agent-policy routes registered.
#   AC-10 retrieval.agent_views.enabled is a registered knob (default false).
#   tests unit + conformance pass.
set -uo pipefail
cd "$(dirname "$0")/../.."

fails=0
ok()    { printf 'OK   %s\n' "$*"; }
failc() { printf 'FAIL %s\n' "$*"; fails=$((fails+1)); }
skip()  { printf 'SKIP %s\n' "$*"; }

# ── AC-3: Dockyard bump + real RequestMeta symbol (M5) ──────────────────────────
if grep -Eq 'hurtener/dockyard v1\.8\.[0-9]' go.mod; then
  ok "AC-3: go.mod pins dockyard v1.8.x"
elif grep -q 'hurtener/dockyard v1.7.3' go.mod; then
  skip "AC-3: dockyard still v1.7.3 (bump not landed)"
else
  failc "AC-3: unexpected dockyard pin in go.mod"
fi
if grep -Rq 'MetaFromContext' internal sdk cmd 2>/dev/null; then
  failc "AC-3: MetaFromContext present — should be server.RequestMeta (M5)"
else
  ok "AC-3: no MetaFromContext placeholder (correct)"
fi

# ── AC-1: Scope.Agent exists and is inert in scope-WHERE builders ───────────────
if grep -q 'Agent' internal/identity/identity.go; then
  ok "AC-1: identity.Scope has an Agent field"
else
  skip "AC-1: Scope.Agent not added yet"
fi
agent_leak=0
for f in internal/store/pgstore/scope.go internal/store/sqlitestore/scope.go; do
  grep -Eiq 'agent' "$f" && { agent_leak=1; failc "AC-1: 'agent' referenced in $f (must be inert)"; }
done
[ "$agent_leak" -eq 0 ] && ok "AC-1: no agent reference in either driver's scope.go (inert)"

# ── AC-2: no agent column / no scope-table migration ────────────────────────────
if grep -Rql 'ADD COLUMN.*agent\|agent_id .*NOT NULL' internal/store/migrations 2>/dev/null \
   | grep -qv '0013_topic_views'; then
  failc "AC-2: an agent column was added to a scope-table migration"
else
  ok "AC-2: no agent column added to any scope table"
fi

# ── AC-4: policy store seam + migration on both drivers (D-151: topic_views) ────
if grep -q 'TopicViewStore' internal/store/store.go; then
  ok "AC-4: TopicViewStore seam declared"
else
  skip "AC-4: TopicViewStore not declared yet"
fi
if grep -q 'TopicViews()' internal/store/store.go; then
  ok "AC-4: Store.TopicViews() accessor declared"
else
  skip "AC-4: Store.TopicViews() not declared yet"
fi
mig=0
for d in sqlite postgres; do
  [ -f "internal/store/migrations/$d/0013_topic_views.sql" ] || { mig=1; skip "AC-4: $d 0013 migration missing"; }
done
[ "$mig" -eq 0 ] && ok "AC-4: 0013_topic_views migration present for both drivers"

# ── AC-7: reuse ae6's filter, resolver-only in ae1 ──────────────────────────────
AF=internal/retrieval/agentfilter.go
if [ -f "$AF" ]; then
  grep -q 'resolveAgentTopics' "$AF" && ok "AC-7: resolveAgentTopics defined" \
    || failc "AC-7: resolveAgentTopics missing"
  if grep -q 'func.*filterByTopicOwnScope' "$AF"; then
    failc "AC-7: ae1 redefines filterByTopicOwnScope — must reuse ae6's"
  else
    ok "AC-7: ae1 does not redefine the topic filter (reuses ae6)"
  fi
else
  skip "AC-7: agentfilter.go not built yet"
fi

# ── AC-8: agent_id intake on HTTP + SDK retrieve contracts ──────────────────────
intake=0
for f in internal/api/retrieve_handler.go sdk/stowage/types.go; do
  grep -q 'agent_id' "$f" || { intake=1; skip "AC-8: $f missing agent_id intake"; }
done
[ "$intake" -eq 0 ] && ok "AC-8: agent_id present on HTTP+SDK retrieve contracts"

# ── AC-9: admin surfaces {HTTP,MCP} ─────────────────────────────────────────────
grep -Rq 'memory_agent_policy' internal/mcpserver 2>/dev/null \
  && ok "AC-9: memory_agent_policy MCP tool registered" \
  || skip "AC-9: memory_agent_policy MCP tool not present yet"
[ -f internal/api/agentpolicy_handler.go ] \
  && ok "AC-9: HTTP agent-policy handler present" \
  || skip "AC-9: HTTP agent-policy handler not present yet"

# ── AC-10: knob registered (D-151: retrieval.agent_views.enabled) ──────────────
if grep -q 'agent_views\.enabled\|agent_views_enabled\|AgentViewsEnabled' internal/config/config.go; then
  ok "AC-10: retrieval.agent_views.enabled present in config"
else
  skip "AC-10: retrieval.agent_views.enabled not present yet"
fi

# ── tests ───────────────────────────────────────────────────────────────────────
if [ -f "$AF" ]; then
  go test ./internal/retrieval/ -run AgentFilter -count=1 >/dev/null 2>&1 \
    && ok "AC-5/6: retrieval agent-filter unit tests pass" \
    || failc "AC-5/6: retrieval agent-filter unit tests fail"
else
  skip "AC-5/6: agent-filter unit tests not present yet"
fi
if [ -f test/integration/agentfilter_test.go ]; then
  go test ./test/integration/ -run AgentFilter -count=1 >/dev/null 2>&1 \
    && ok "AC-5/6: integration agent-narrow + fail-open tests pass" \
    || failc "AC-5/6: integration agent-filter tests fail"
else
  skip "AC-5/6: integration agent-filter tests not present yet"
fi

exit "$fails"
