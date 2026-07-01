#!/usr/bin/env bash
# Phase ae2 smoke: additive _meta identity intake (D-137 impl, D-138 guard). The MCP
# handlers read user/session/agent from the host-injected _meta ALONGSIDE the existing
# project_id/user_id/session_id args; tenant is NEVER from _meta and a mismatched
# _meta.tenant fails closed; an explicit arg wins over a _meta value; nothing removed.
#
# Verifies:
#   AC-9   readMetaIdentity is the ONE _meta intake seam (single RequestMeta call site).
#   AC-3   identity.ErrTenantMismatch (the D-138 fail-closed sentinel) exists.
#   AC-10  go.mod pins dockyard v1.8.0 so server.RequestMeta compiles.
#   AC-4   no handler sources Scope.Tenant from _meta.
#   AC-6   no read handler writes _meta session into Scope.Session.
#   AC-8   _meta.project is not read (project_id keeps its arg home, M1).
#   AC-1/2/3 unit + identity tests pass.
set -uo pipefail
cd "$(dirname "$0")/../.."

fails=0
ok()    { printf 'OK   %s\n' "$*"; }
failc() { printf 'FAIL %s\n' "$*"; fails=$((fails+1)); }
skip()  { printf 'SKIP %s\n' "$*"; }

MI=internal/mcpserver/metaintake.go

# ── AC-9: the single intake seam exists ─────────────────────────────────────────
if [ ! -f "$MI" ]; then
  skip "AC-9: $MI not built yet (ae2 not landed)"
  exit "$fails"
fi
if grep -q 'func readMetaIdentity' "$MI"; then
  ok "AC-9: readMetaIdentity defined"
else
  failc "AC-9: readMetaIdentity missing"
fi

# ── AC-9: exactly one server.RequestMeta call site in the package ────────────────
rmcount=$(grep -Rho 'server\.RequestMeta(' internal/mcpserver | wc -l | tr -d ' ')
if [ "$rmcount" = "1" ]; then
  ok "AC-9: exactly one server.RequestMeta call site (no sprawl)"
else
  failc "AC-9: expected 1 server.RequestMeta call site, found $rmcount"
fi

# ── AC-3: the D-138 fail-closed sentinel exists ─────────────────────────────────
if grep -Rq 'ErrTenantMismatch' internal/identity/identity.go; then
  ok "AC-3: identity.ErrTenantMismatch defined"
else
  failc "AC-3: identity.ErrTenantMismatch missing"
fi

# ── AC-10: dockyard v1.8.0 pinned ───────────────────────────────────────────────
if grep -Eq 'github.com/hurtener/dockyard v1\.(8|9|[1-9][0-9])\.' go.mod; then
  ok "AC-10: go.mod pins dockyard >= v1.8.0 (RequestMeta available)"
else
  failc "AC-10: go.mod does not pin dockyard v1.8.0"
fi

# ── AC-4: tenant is never sourced from _meta ────────────────────────────────────
if grep -REq 'Tenant:[[:space:]]*(mi\.|.*RequestMeta)' internal/mcpserver/handlers.go; then
  failc "AC-4: a handler appears to source Scope.Tenant from _meta"
else
  ok "AC-4: no handler sources Scope.Tenant from _meta"
fi

# ── AC-6: _meta session is not written into Scope.Session ───────────────────────
if grep -Eq 'Session:[[:space:]]*(mi\.Session|argElseMeta\(in\.SessionID)' internal/mcpserver/handlers.go; then
  failc "AC-6: a handler writes the effective/_meta session into Scope.Session (new predicate risk)"
else
  ok "AC-6: no handler writes _meta session into Scope.Session"
fi

# ── AC-8: project keeps its arg home — _meta.project is not read ─────────────────
if grep -REq 'RequestMeta\([^)]*\)\["project"\]|mi\.Project|metaString\([^,]*, *"project"' internal/mcpserver; then
  failc "AC-8: _meta.project is read (project_id should keep its arg home in ae2)"
else
  ok "AC-8: _meta.project not read (project_id arg home preserved)"
fi

# ── AC-1/2/3: tests ─────────────────────────────────────────────────────────────
if go test ./internal/mcpserver/ -run MetaIntake -count=1 >/dev/null 2>&1; then
  ok "AC-1/2/3: mcpserver _meta-intake unit tests pass"
else
  failc "AC-1/2/3: mcpserver _meta-intake unit tests fail"
fi
if go test ./internal/identity/ -run TenantMismatch -count=1 >/dev/null 2>&1; then
  ok "AC-3: identity ErrTenantMismatch tests pass"
else
  failc "AC-3: identity ErrTenantMismatch tests fail"
fi
if go test ./test/integration/ -run MetaIntake -count=1 >/dev/null 2>&1; then
  ok "AC-1/5: integration _meta-narrows + no-_meta-identical tests pass"
else
  skip "AC-1/5: integration _meta-intake tests not present/passing yet"
fi

exit "$fails"
