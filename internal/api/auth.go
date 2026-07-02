package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/hurtener/stowage/internal/auth"
	"github.com/hurtener/stowage/internal/identity"
)

// authKeyCtxKey is the context key for the authenticated *auth.Key.
type authKeyCtxKey struct{}

// authMiddleware authenticates the request via the server's *auth.Authenticator
// (ae7, D-067) — keyring or JWT depending on how it was built — and stores
// the resolved key and scope on the request context.
//
// If requireAdmin is true, the resolved Role must be RoleAdmin; otherwise any
// valid credential (agent or admin) is accepted.
//
// Never logs credential material (CLAUDE.md §7).
func (s *Server) authMiddleware(next http.HandlerFunc, requireAdmin bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		hdr := r.Header.Get("Authorization")
		scope, role, keyID, err := s.authn.Authenticate(r.Context(), hdr, r.Header.Get(auth.SessionHeader))
		if err != nil {
			respondJSON(w, http.StatusUnauthorized, errBody("authentication failed: "+auth.ReasonForWire(err)))
			return
		}

		if requireAdmin && role != auth.RoleAdmin {
			respondJSON(w, http.StatusForbidden, errBody("admin role required"))
			return
		}

		// Back-compat: synthesize a *auth.Key so keyFromContext/scopeFromRequest
		// keep compiling and behaving on BOTH modes. In ModeJWT this is a
		// synthetic view over the verified Scope, not a real stored key — ID
		// stays "" there (ae9, D-149: the "key" topic-view subject fallback
		// simply never resolves for a JWT-mode caller; see auth.Authenticate).
		key := &auth.Key{TenantID: scope.Tenant, Role: role, ID: keyID}
		ctx := context.WithValue(r.Context(), authKeyCtxKey{}, key)
		// The FULL verified Scope (Tenant/User/Session in ModeJWT) is set
		// alongside (P3) — ae7's core deliverable; ae8 wires read handlers to
		// consume Scope.User/Session directly.
		ctx = identity.WithScope(ctx, scope)
		next.ServeHTTP(w, r.WithContext(ctx))
	}
}

// keyFromContext retrieves the authenticated *auth.Key from context.
// Panics if auth middleware was not applied — callers can rely on this being
// set on any route that uses authMiddleware.
func keyFromContext(ctx context.Context) *auth.Key {
	k, ok := ctx.Value(authKeyCtxKey{}).(*auth.Key)
	if !ok || k == nil {
		panic("api: keyFromContext called without authMiddleware")
	}
	return k
}

// identityArgs carries one single-user HTTP handler's caller-supplied D-125
// identity dimensions into resolveScope (ae8, D-148). A handler with no
// project/user/session/agent field for its shape simply leaves it zero.
type identityArgs struct {
	Project string
	User    string
	Session string
	Agent   string
}

// resolveScope builds the effective READ identity.Scope for a single-user
// HTTP handler through the ONE cross-surface resolver, identity.
// ResolveReadScope (ae8, D-148/D-067/D-073): the tenant from the
// authenticated credential, the verified JWT claims (ae7, ModeJWT — the
// Scope authMiddleware already placed on context) as the Claim* sources, and
// the caller-supplied project/user/session arguments (D-125) as the Arg*
// sources. HTTP has no _meta channel (D-140), so the Meta* fields stay zero.
//
// In ModeKeyring the context Scope carries only Tenant (no verified user), so
// CredUser/ClaimUser/ClaimSession stay "" and the resolver falls back to its
// fully back-compat args-only branch — byte-identical to pre-ae8 HTTP. In
// ModeJWT the verified Scope's User IS the credential's own pinned user (a
// JWT's `user` claim is not a separate on-behalf-of assertion, D-137 Harbor
// evidence), so CredUser and ClaimUser are the same source value.
//
// Returns the resolved Scope and the effective session (D-150: NEVER placed
// on the returned Scope — route it to the caller's own relevance sink).
// agent (arg.Agent) is the D-140-sanctioned HTTP arg channel (there is no
// _meta to source it from); it is fed into the resolver's MetaAgent slot
// (the generic "read-time host-identity channel" — HTTP's own per-surface
// equivalent, D-140) rather than assigned post-hoc, so a strict-posture
// caller that supplies only an agent (no user) still satisfies the step-6
// presence gate inside the ONE resolve call.
func (s *Server) resolveScope(r *http.Request, arg identityArgs) (identity.Scope, string, error) {
	authKey := keyFromContext(r.Context())
	src := identity.IdentitySources{
		Tenant:     authKey.TenantID,
		ArgUser:    arg.User,
		ArgProject: arg.Project,
		ArgSession: arg.Session,
		MetaAgent:  arg.Agent,
	}
	if verified, verr := identity.FromContext(r.Context()); verr == nil {
		src.CredUser = verified.User
		src.ClaimTenant = verified.Tenant
		src.ClaimUser = verified.User
		src.ClaimSession = verified.Session
	}
	return identity.ResolveReadScope(src, s.resolveOpts)
}

// scopeFromRequest builds the read scope for a GET single-user-tier handler
// from its query-string args (?project_id=&user_id=), through resolveScope.
// Use this for GET handlers; POST handlers with a JSON body call resolveScope
// directly with their decoded body fields instead.
func (s *Server) scopeFromRequest(r *http.Request) (identity.Scope, error) {
	q := r.URL.Query()
	scope, _, err := s.resolveScope(r, identityArgs{Project: q.Get("project_id"), User: q.Get("user_id")})
	return scope, err
}

// respondScopeError writes the appropriate HTTP status for an
// identity.ResolveReadScope failure (ae8): an identity conflict/refusal is a
// 403 (authenticated but not authorized for the asserted/omitted identity);
// any other resolution failure (e.g. an empty tenant, which should not occur
// past authMiddleware) is a 400.
func respondScopeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, identity.ErrTenantMismatch):
		respondJSON(w, http.StatusForbidden, errBody("scope resolution: tenant mismatch"))
	case errors.Is(err, identity.ErrUserConflict):
		respondJSON(w, http.StatusForbidden, errBody("scope resolution: user conflict"))
	case errors.Is(err, identity.ErrIdentityRequired):
		respondJSON(w, http.StatusForbidden, errBody("scope resolution: identity required (read_posture=strict)"))
	default:
		respondJSON(w, http.StatusBadRequest, errBody("scope resolution failed"))
	}
}
