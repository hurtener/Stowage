#!/usr/bin/env bash
# Smoke for Phase 30 — read/write scope enforcement (multi-user-per-tenant isolation, D-124/125).
# Asserts the load-bearing invariants survive; behavioural proof is in the package tests
# (conformance RecordAppendScopeFill + retrieval TestRetrieve_UserScopeIsolation).
set -uo pipefail
cd "$(dirname "$0")/../.."
fails=0
ok(){ echo "OK   $*"; }; failc(){ echo "FAIL $*"; fails=$((fails+1)); }
has(){ grep -q "$1" "$2" && ok "$3" || failc "$3 ($2 :: $1)"; }

# D-124 — write: Append fills per-record scope (scope wins, record fills the gap), both drivers.
has "scopeOrRecord(scope.Project, rec.ProjectID)" internal/store/sqlitestore/records.go "sqlite Append fills per-record project/user/session"
has "scopeOrRecord(scope.Project, rec.ProjectID)" internal/store/pgstore/records.go "pgstore Append fills per-record project/user/session"
has "func scopeOrRecord" internal/store/sqlitestore/records.go "sqlite scopeOrRecord helper (scope-authoritative)"
has "func scopeOrRecord" internal/store/pgstore/records.go "pgstore scopeOrRecord helper"

# B1 — pipeline stamps the derived MEMORY's scope from the record (not tenant-only), so the
# D-125 read filter has a user to match; + the commit binds scopeOrRecord (scope wins, record fills).
has "Project: rec.ProjectID, User: rec.UserID" internal/pipeline/pipeline.go "pipeline flush scope carries record project/user (B1)"
has "scopeOrRecord(scope.User, mem.UserID)" internal/store/sqlitestore/memories.go "sqlite memory commit scope-or-record fallback (B1)"
has "scopeOrRecord(scope.User, mem.UserID)" internal/store/pgstore/memories.go "pgstore memory commit scope-or-record fallback (B1)"
# B1 follow-on — the backfill embed sweep (guaranteed-recovery path) carries the memory's project/user.
has "Project: m.ProjectID, User: m.UserID" internal/reconcile/embedder.go "embedder backfill carries memory project/user (B1 follow-on)"
# B1 follow-on — the reflection sweep writes the distilled strategy memory under the trajectory owner.
has "trajScope := identity.Scope{Tenant: tenant, Project: traj.ProjectID, User: traj.UserID}" internal/lifecycle/reflect.go "reflection batch carries trajectory owner scope (B1 follow-on)"

# B2 — non-lossy result-cache scope key + ancestor-aware generation (per-user keys, tenant-wide bust).
has "func scopeCacheKey" internal/retrieval/cache.go "non-lossy cache key helper (B2)"
has "func (c \*ResultCache) scopeGen" internal/retrieval/cache.go "ancestor-aware cache generation (B2)"
has "scopeCacheKey(scope)" internal/retrieval/hotset.go "hotset uses non-lossy scope key (B2)"

# D-125 — read: retrieve builds the scope from caller-supplied project/user across all surfaces.
has "Project: req.ProjectID, User: req.UserID" internal/api/retrieve_handler.go "HTTP retrieve scopes by project/user"
has 'json:"project_id"' internal/api/retrieve_handler.go "HTTP retrieve request has project_id"
has "c.callScope(req.ProjectID, req.UserID," sdk/stowage/embedded.go "SDK retrieve scopes by project/user (via callScope -> ResolveReadScope, ae8)"
has 'ProjectID  string `json:"project_id,omitempty"`' sdk/stowage/types.go "SDK RetrieveRequest has project_id"
has "resolveScope(svc, ctx, scopeArgs{Session: in.SessionID})" internal/mcpserver/handlers.go "MCP retrieve scopes by identity (via resolveScope; project/user from _meta/JWT since ae2b)"
# ae2b (D-140/M1, docs/plans/phase-ae2b-contract-removal.md) is the NAMED, later
# phase that retires RetrieveInput's project_id/user_id args in favor of
# _meta/JWT-only MCP identity — this specific check is superseded by design,
# not a regression; asserting the field's ABSENCE (post-ae2b) instead of its
# presence keeps this line a real drift-catcher rather than a stale assertion.
if grep -q 'type RetrieveInput struct' internal/mcpserver/contracts.go && \
   ! awk '/type RetrieveInput struct/{f=1} f&&/^}/{exit} f' internal/mcpserver/contracts.go | grep -q 'json:"project_id\|json:"user_id'; then
  ok "MCP RetrieveInput has no project_id/user_id (ae2b, D-140 — _meta/JWT only)"
else
  failc "MCP RetrieveInput project_id/user_id state does not match ae2b (internal/mcpserver/contracts.go)"
fi

# B3 — the OTHER single-user read+mutate surfaces scope by project/user (HTTP/MCP/SDK).
has "func (s \*Server) scopeFromRequest" internal/api/auth.go "HTTP shared query-param scope helper (B3; ae8 method on *Server)"
has "s.resolveScope(r, identityArgs" internal/api/playbook_handler.go "HTTP playbook scoped (B3; via resolveScope, ae8)"
has "s.resolveScope(r, identityArgs" internal/api/episodes_handler.go "HTTP episodes scoped (B3; via resolveScope, ae8)"
has "Project: req.ProjectID, User: req.UserID" internal/api/review_handler.go "HTTP review resolve scoped — MUTATE (B3)"
has "func (c \*embeddedClient) callScope" sdk/stowage/embedded.go "SDK per-call scope helper (B3)"
has "func WithUser" sdk/stowage/http.go "SDK WithUser construction option (B3)"
has "func (c \*httpClient) effScope" sdk/stowage/http.go "SDK HTTP client honors construction scope (B-1)"
has "resolveScope(svc, ctx, scopeArgs" internal/mcpserver/handlers.go "MCP handlers scope per-request via resolveScope (B3; project/user from _meta/JWT since ae2b)"
# Fail-closed hnsw sub-scope filter (Finding 3 hardening) + rollup owner-scope (Finding 4).
has "Fail CLOSED for isolation" internal/vindex/hnsw/driver.go "hnsw filter fails closed on missing meta"
has "ownerScope := identity.Scope{Tenant: scope.Tenant" internal/lifecycle/rollup.go "rollup digest inherits session owner scope"

# Regression guards present.
has "testRecordAppendScopeFill" internal/store/conformance/conformance.go "write-scope conformance present"
has "TestRetrieve_UserScopeIsolation" internal/retrieval/retrieval_test.go "read-isolation guard present (lexical+vector+cache)"
has "TestFlushScopeFromRecord" internal/pipeline/buffer_test.go "B1 pipeline write-path guard present"
has "TestResultCache_AncestorInvalidation" internal/retrieval/retrieval_test.go "B2 cache hierarchical-invalidation guard present"
has "TestScopeParity_ReviewList_AllSurfaces" test/integration/scope_parity_test.go "cross-surface scope-parity guard present (B3 AC#3)"
has "TestScopeParity_ReviewResolve_CrossUserDenied" test/integration/scope_parity_test.go "cross-user MUTATE-denied parity guard present (B-2)"
has "TestScopeParity_HTTPConstructionScope" test/integration/scope_parity_test.go "HTTP construction-scope guard present (B-1)"
has "TestEmbedder_BackfillSweep_PreservesUserScope" internal/reconcile/embedder_test.go "backfill scope-preservation guard present (B1 follow-on)"
has "TestReflectSweep_BatchCarriesTrajectoryOwner" internal/lifecycle/reflect_test.go "reflection owner-scope guard present (B1 follow-on)"

go build ./... >/dev/null 2>&1 && ok "build green" || failc "build"
total=39
echo "phase-30 smoke: $((total - fails)) passed, $fails failed"
exit "$fails"
