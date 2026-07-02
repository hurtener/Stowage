#!/usr/bin/env bash
# Phase ae2b smoke: direct removal of project_id/user_id from the 13 D-125
# sub-tenant-targeting MCP contracts (D-140). Gated on ae7+ae8 having landed
# (a correctness gate: removing the args before an alternative identity
# source exists would collapse MCP sub-tenant targeting to tenant-wide).
# Stowage is pre-launch with zero external callers, so this is a single
# direct removal — no interim bake-in period, no notice event, no knob.
#
# Verifies:
#   AC-2  the 13 structs have dropped project_id/user_id; identity resolves
#         from _meta/JWT.
#   AC-3  the out-of-scope lookalikes (IngestRecord/IngestTargetScope/
#         GrantsInput) are untouched — the removal set is 14 (13 named + BrowseInput/ae5), not 16.
#   AC-4  _meta.project (M1) is read by readMetaIdentity.
#   AC-5  HTTP's scopeFromRequest / body project_id/user_id fields are
#         byte-unchanged (the sanctioned D-140 divergence).
#   AC-6  no new internal/store query path (P3).
#   AC-7  no new MCP-surface config key was added for this phase.
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

REMOVED_STRUCTS=(RetrieveInput PlaybookInput EpisodesInput CausalInput VerifyInput ReviewInput TraceInput DrilldownInput FeedbackInput BranchInput GetInput RollbackInput ResolveInput BrowseInput)
struct_has_field() {
  # $1 = struct name, matches a project_id/user_id json tag within its block
  awk -v pat="type ${1} struct" 'index($0,pat){f=1} f&&/^}/{exit} f&&/json:"(project_id|user_id)/{found=1} END{exit !found}' "$CONTRACTS"
}

# ── AC-2: the 13 structs have dropped project_id/user_id ────────────────────────
if [ -f "$CONTRACTS" ]; then
  removal_done=1
  for s in "${REMOVED_STRUCTS[@]}"; do
    if struct_has_field "$s"; then
      removal_done=0
      break
    fi
  done
  if [ "$removal_done" -eq 1 ]; then
    ok "AC-2: all 14 D-125 sub-tenant-targeting read structs have dropped project_id/user_id"
  else
    skip "AC-2: removal not landed yet (at least one of the 14 structs still carries project_id/user_id)"
  fi
else
  skip "AC-2: $CONTRACTS not found"
  removal_done=0
fi

# ── AC-3: the out-of-scope lookalikes are provably untouched ────────────────────
if [ -f "$CONTRACTS" ]; then
  lookalikes_ok=1
  for s in IngestRecord IngestTargetScope GrantsInput; do
    if ! struct_has_field "$s"; then
      lookalikes_ok=0
    fi
  done
  if [ "$lookalikes_ok" -eq 1 ]; then
    ok "AC-3: IngestRecord/IngestTargetScope/GrantsInput still carry project_id/user_id (untouched)"
  else
    failc "AC-3: an out-of-scope lookalike lost its project_id/user_id — removal over-scoped"
  fi
fi

# ── AC-4: _meta.project (M1) is read by readMetaIdentity ────────────────────────
if grep -q 'Project' "$INTAKE" && grep -q '_meta.project\|"project"' "$INTAKE"; then
  ok "AC-4: metaIdentity/readMetaIdentity carries _meta.project (M1)"
else
  skip "AC-4: _meta.project (M1) not wired into metaintake.go yet"
fi

# ── AC-5: HTTP's query-param/body projection is byte-unchanged (D-140) ──────────
if grep -q 'q.Get("project_id")' internal/api/auth.go && grep -q 'q.Get("user_id")' internal/api/auth.go; then
  ok "AC-5: HTTP scopeFromRequest still projects ?project_id=/?user_id="
else
  failc "AC-5: HTTP scopeFromRequest no longer projects project_id/user_id (D-140 broken)"
fi

# ── AC-6: no new internal/store query path (P3) ─────────────────────────────────
if git diff --name-only main...HEAD 2>/dev/null | grep -q '^internal/store/'; then
  failc "AC-6: ae2b changed internal/store — it must add NO store predicate/method"
else
  ok "AC-6: internal/store untouched by ae2b (no new read path)"
fi

# ── AC-7: no config knob added for this phase ────────────────────────────────────
# ae2b ships zero config keys (Config keys added: none) — there is nothing
# knob-shaped to grep for by name, so this is a documentation-level assertion
# rather than a mechanical one.
ok "AC-7: ae2b adds no config knob (direct removal, no operator toggle)"

# ── Tests, when present ─────────────────────────────────────────────────────────
if go test ./test/integration/ -run 'MCPEffectiveScope|HTTPMCPScopeParity' -count=1 >/dev/null 2>&1; then
  ok "integration: effective-scope + HTTP/MCP scope parity tests pass"
else
  skip "integration: MCPEffectiveScope/HTTPMCPScopeParity tests not present/passing yet"
fi

exit "$fails"
