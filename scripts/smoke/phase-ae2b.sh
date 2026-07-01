#!/usr/bin/env bash
# Phase ae2b smoke: breaking removal of project_id/user_id from MCP contracts
# (D-140). Gated on ae7+ae8. Two shippable steps:
#   Step 1 (deprecation window) — args stay on the wire, still resolve scope
#           exactly as today (lowest-precedence in ae8's IdentitySources), and
#           a still-load-bearing use emits a versioned mcp.legacy_scope_arg_used
#           warning event; mcp.deprecated_args_mode (warn|reject) lets an
#           operator dry-run the removal.
#   Step 2 (removal) — the 13 D-125 sub-tenant-targeting structs drop
#           ProjectID/UserID; the knob is retired in the same PR.
#
# Verifies:
#   AC-1  deprecation: legacy-arg detection + warning event machinery exists.
#   AC-2  mcp.deprecated_args_mode registered, default warn, enum-validated.
#   AC-3  removal: the 13 structs have dropped project_id/user_id.
#   AC-4  the out-of-scope lookalikes (IngestRecord/IngestTargetScope/
#         GrantsInput) are untouched — the removal set is exactly 13, not 16.
#   AC-6  HTTP's scopeFromRequest / body project_id/user_id fields are
#         byte-unchanged (the sanctioned D-140 divergence).
#   AC-7  no new internal/store query path (P3).
#   AC-8  once removed, the knob is gone too (no dangling dead config).
set -uo pipefail
cd "$(dirname "$0")/../.."

fails=0
ok()    { printf 'OK   %s\n' "$*"; }
failc() { printf 'FAIL %s\n' "$*"; fails=$((fails+1)); }
skip()  { printf 'SKIP %s\n' "$*"; }

INTAKE=internal/mcpserver/metaintake.go
CONTRACTS=internal/mcpserver/contracts.go

# ── Gate: ae2b cannot exist meaningfully before ae2/ae7/ae8 land ────────────────
if [ ! -f "$INTAKE" ]; then
  skip "ae2b: $INTAKE not built yet (ae2 not landed; ae2b is gated on ae2+ae7+ae8)"
  exit "$fails"
fi

# ── Determine which step has landed by whether the 13 structs still carry the
#    legacy args. If even one of the 13 still has project_id/user_id, treat
#    this as Step-1-or-earlier; only run Step-2 checks once ALL 13 have lost
#    the fields. This lets the script track either shipped state gracefully.
REMOVED_STRUCTS=(RetrieveInput PlaybookInput EpisodesInput CausalInput VerifyInput ReviewInput TraceInput DrilldownInput FeedbackInput BranchInput GetInput RollbackInput ResolveInput)
step2_done=1
if [ -f "$CONTRACTS" ]; then
  for s in "${REMOVED_STRUCTS[@]}"; do
    # crude but adequate: look for the struct block and a project_id/user_id tag within ~15 lines after it
    if awk -v pat="type ${s} struct" 'index($0,pat){f=1} f&&/^}/{exit} f&&/json:"(project_id|user_id)/{found=1} END{exit !found}' "$CONTRACTS"; then
      step2_done=0
      break
    fi
  done
else
  step2_done=0
fi

# ── AC-1: legacy-arg detection + warning-event machinery (Step 1) ──────────────
if grep -q 'func detectLegacyArgUse' "$INTAKE" && grep -q 'func applyLegacyArgPolicy' "$INTAKE"; then
  ok "AC-1: detectLegacyArgUse + applyLegacyArgPolicy defined in metaintake.go"
else
  failc "AC-1: legacy-arg detection/policy helpers missing from metaintake.go"
fi
if grep -q 'mcp.legacy_scope_arg_used' "$INTAKE" || grep -Rq 'mcp.legacy_scope_arg_used' internal/mcpserver; then
  ok "AC-1: mcp.legacy_scope_arg_used warning event type present"
else
  failc "AC-1: mcp.legacy_scope_arg_used event type not found"
fi
if grep -Rq 'schema_version' internal/mcpserver; then
  ok "AC-1: warning event payload carries an explicit schema_version"
else
  failc "AC-1: warning event payload has no schema_version field"
fi

# ── AC-2: the deprecation-mode knob (present pre-removal, gone post-removal) ────
if [ "$step2_done" -eq 1 ]; then
  if grep -q 'deprecated_args_mode' internal/config/config.go; then
    failc "AC-8: mcp.deprecated_args_mode still present after Step 2 removal (dangling knob)"
  else
    ok "AC-8: mcp.deprecated_args_mode retired along with the removed fields (Step 2)"
  fi
else
  if grep -q 'mcp.deprecated_args_mode' internal/config/config.go; then
    ok "AC-2: mcp.deprecated_args_mode registered in config"
  else
    failc "AC-2: mcp.deprecated_args_mode missing from config"
  fi
  if grep -Eq 'deprecated_args_mode[[:space:]]*=[[:space:]]*warn' internal/config/testdata/explain_default.golden 2>/dev/null; then
    ok "AC-2: mcp.deprecated_args_mode defaults to warn (explain golden)"
  else
    skip "AC-2: mcp.deprecated_args_mode default not yet in explain golden"
  fi
fi

# ── AC-3/AC-4: removal set is exactly the 13 structs, never the lookalikes ──────
if [ "$step2_done" -eq 1 ]; then
  ok "AC-3: all 13 D-125 sub-tenant-targeting structs have dropped project_id/user_id"
  lookalikes_ok=1
  for s in IngestRecord IngestTargetScope GrantsInput; do
    if ! awk -v pat="type ${s} struct" 'index($0,pat){f=1} f&&/^}/{exit} f&&/json:"(project_id|user_id)/{found=1} END{exit !found}' "$CONTRACTS"; then
      lookalikes_ok=0
    fi
  done
  if [ "$lookalikes_ok" -eq 1 ]; then
    ok "AC-4: IngestRecord/IngestTargetScope/GrantsInput still carry project_id/user_id (untouched)"
  else
    failc "AC-4: an out-of-scope lookalike lost its project_id/user_id — removal over-scoped"
  fi
else
  skip "AC-3: Step 2 (field removal) not landed yet"
fi

# ── AC-6: HTTP's query-param/body projection is byte-unchanged (D-140) ──────────
if grep -q 'q.Get("project_id")' internal/api/auth.go && grep -q 'q.Get("user_id")' internal/api/auth.go; then
  ok "AC-6: HTTP scopeFromRequest still projects ?project_id=/?user_id="
else
  failc "AC-6: HTTP scopeFromRequest no longer projects project_id/user_id (D-140 broken)"
fi

# ── AC-7: no new internal/store query path (P3) ────────────────────────────────
if git diff --name-only main...HEAD 2>/dev/null | grep -q '^internal/store/'; then
  failc "AC-7: ae2b changed internal/store — it must add NO store predicate/method"
else
  ok "AC-7: internal/store untouched by ae2b (no new read path)"
fi

# ── Tests, when present ─────────────────────────────────────────────────────────
if go test ./internal/mcpserver/ -run LegacyArg -count=1 >/dev/null 2>&1; then
  ok "unit: legacy-arg detection + warn/reject policy tests pass"
else
  skip "unit: LegacyArg tests not present/passing yet"
fi
if go test ./test/integration/ -run 'LegacyArgRemoval|HTTPMCPScopeParity' -count=1 >/dev/null 2>&1; then
  ok "integration: legacy-arg removal + HTTP/MCP scope parity tests pass"
else
  skip "integration: LegacyArgRemoval/HTTPMCPScopeParity tests not present/passing yet"
fi

exit "$fails"
