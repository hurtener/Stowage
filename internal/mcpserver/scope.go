package mcpserver

// scope.go — Phase ae8 (D-148): the ONE resolveScope helper every MCP READ
// handler calls, replacing the ~15 hand-rolled
// `identity.Scope{Tenant: scope.Tenant, Project: in.ProjectID, User:
// metaElseArg(mi.User, in.UserID), Agent: mi.Agent}` literals with a single
// call into the cross-surface resolver, identity.ResolveReadScope
// (D-067/D-073 one logic core). ae2's readMetaIdentity/metaElseArg
// (metaintake.go) are UNCHANGED and still the ONLY intake path for WRITE/
// admin handlers, which guard-only (discard the identity) exactly as ae2
// built them — this file does not touch that.
//
// resolveScope performs the SAME D-138 tenant guard ae2's readMetaIdentity
// does, but via identity.ResolveReadScope's step 1 (which reuses
// identity.ErrTenantMismatch) rather than a second, duplicate check — one
// tenant guard, realized in the shared resolver.

import (
	"context"
	"fmt"

	"github.com/hurtener/stowage/internal/identity"
)

// scopeArgs carries one handler's in-band D-125 tool args into resolveScope.
// A handler that has no project/user/session arg (e.g. memory_suggestions)
// simply leaves the corresponding field zero.
type scopeArgs struct {
	Project string
	User    string
	Session string
}

// resolveScope resolves the credential scope (svc.ScopeFn), reads the
// host-injected _meta (ae2's server.RequestMeta), and merges both with arg
// through the ONE cross-surface resolver, identity.ResolveReadScope (ae8,
// D-148/D-067/D-073). CanAssertUser is always false today — the pre-ae7 MCP
// keyring pins no user; a later phase populates it from a per-credential
// capability without touching the resolver.
//
// Returns the resolved read Scope and the effective session (D-150: NEVER
// placed on the returned Scope — route it to the caller's own relevance sink,
// e.g. retrieval.Request.SessionID / playbook.Options.SessionID).
func resolveScope(svc *Services, ctx context.Context, arg scopeArgs) (identity.Scope, string, error) {
	cred, err := svc.ScopeFn(ctx)
	if err != nil {
		return identity.Scope{}, "", fmt.Errorf("resolve scope: %w", err)
	}
	m := requestMeta(ctx) // ae2's single _meta seam (metaintake.go); nil when unsent
	src := identity.IdentitySources{
		Tenant:      cred.Tenant,
		MetaTenant:  metaString(m, "tenant"),
		MetaUser:    metaString(m, "user"),
		MetaSession: metaString(m, "session"),
		MetaAgent:   metaString(m, "agent_id"),
		MetaProject: metaString(m, "project_id"),
		ArgUser:     arg.User,
		ArgSession:  arg.Session,
		ArgProject:  arg.Project,
	}
	return identity.ResolveReadScope(src, svc.ResolveOpts)
}
