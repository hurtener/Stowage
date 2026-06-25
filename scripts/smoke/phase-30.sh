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

# B2 — non-lossy result-cache scope key + ancestor-aware generation (per-user keys, tenant-wide bust).
has "func scopeCacheKey" internal/retrieval/cache.go "non-lossy cache key helper (B2)"
has "func (c \*ResultCache) scopeGen" internal/retrieval/cache.go "ancestor-aware cache generation (B2)"
has "scopeCacheKey(scope)" internal/retrieval/hotset.go "hotset uses non-lossy scope key (B2)"

# D-125 — read: retrieve builds the scope from caller-supplied project/user across all surfaces.
has "Project: req.ProjectID, User: req.UserID" internal/api/retrieve_handler.go "HTTP retrieve scopes by project/user"
has 'json:"project_id"' internal/api/retrieve_handler.go "HTTP retrieve request has project_id"
has "scope := c.callScope(req.ProjectID, req.UserID)" sdk/stowage/embedded.go "SDK retrieve scopes by project/user (via callScope)"
has 'ProjectID  string `json:"project_id,omitempty"`' sdk/stowage/types.go "SDK RetrieveRequest has project_id"
has "Project: in.ProjectID, User: in.UserID" internal/mcpserver/handlers.go "MCP retrieve scopes by project/user"
has 'ProjectID  string `json:"project_id,omitempty"`' internal/mcpserver/contracts.go "MCP RetrieveInput has project_id"

# B3 — the OTHER single-user read+mutate surfaces scope by project/user (HTTP/MCP/SDK).
has "func scopeFromRequest" internal/api/auth.go "HTTP shared query-param scope helper (B3)"
has "scope := scopeFromRequest(r)" internal/api/playbook_handler.go "HTTP playbook scoped (B3)"
has "scope := scopeFromRequest(r)" internal/api/episodes_handler.go "HTTP episodes scoped (B3)"
has "Project: req.ProjectID, User: req.UserID" internal/api/review_handler.go "HTTP review resolve scoped — MUTATE (B3)"
has "func (c \*embeddedClient) callScope" sdk/stowage/embedded.go "SDK per-call scope helper (B3)"
has "func WithUser" sdk/stowage/http.go "SDK WithUser construction option (B3)"
has "scope = identity.Scope{Tenant: scope.Tenant, Project: in.ProjectID, User: in.UserID}" internal/mcpserver/handlers.go "MCP handlers merge per-request project/user (B3)"

# Regression guards present.
has "testRecordAppendScopeFill" internal/store/conformance/conformance.go "write-scope conformance present"
has "TestRetrieve_UserScopeIsolation" internal/retrieval/retrieval_test.go "read-isolation guard present (lexical+vector+cache)"
has "TestFlushScopeFromRecord" internal/pipeline/buffer_test.go "B1 pipeline write-path guard present"
has "TestResultCache_AncestorInvalidation" internal/retrieval/retrieval_test.go "B2 cache hierarchical-invalidation guard present"
has "TestScopeParity_ReviewList_AllSurfaces" test/integration/scope_parity_test.go "cross-surface scope-parity guard present (B3 AC#3)"

go build ./... >/dev/null 2>&1 && ok "build green" || failc "build"
total=29
echo "phase-30 smoke: $((total - fails)) passed, $fails failed"
exit "$fails"
