package api

import (
	"context"
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
		scope, role, err := s.authn.Authenticate(r.Context(), hdr, r.Header.Get(auth.SessionHeader))
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
		// synthetic view over the verified Scope, not a real stored key.
		key := &auth.Key{TenantID: scope.Tenant, Role: role}
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

// scopeFromRequest builds the read/mutate scope for a single-user-tier handler:
// the tenant from the authenticated key (the auth boundary), and the optional
// project/user sub-scope supplied per request via ?project_id=/?user_id= query
// params (P3, D-125). Empty params = tenant-wide (back-compat). The store layer
// hard-isolates to this scope via buildScopeWhere. Use this for GET handlers; POST
// handlers with a JSON body carry project_id/user_id as body fields instead.
func scopeFromRequest(r *http.Request) identity.Scope {
	authKey := keyFromContext(r.Context())
	q := r.URL.Query()
	return identity.Scope{
		Tenant:  authKey.TenantID,
		Project: q.Get("project_id"),
		User:    q.Get("user_id"),
	}
}
