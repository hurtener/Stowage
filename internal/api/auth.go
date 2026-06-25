package api

import (
	"context"
	"net/http"
	"strings"

	"github.com/hurtener/stowage/internal/auth"
	"github.com/hurtener/stowage/internal/identity"
)

// authKeyCtxKey is the context key for the authenticated *auth.Key.
type authKeyCtxKey struct{}

// authMiddleware extracts the Bearer token from the Authorization header,
// verifies it against the store keyring (constant-time; CLAUDE.md §7), and
// stores the resolved key and tenant scope on the request context.
//
// If requireAdmin is true, the key must have RoleAdmin; otherwise any valid
// key (agent or admin) is accepted.
//
// Never logs key material (CLAUDE.md §7).
func (s *Server) authMiddleware(next http.HandlerFunc, requireAdmin bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		hdr := r.Header.Get("Authorization")
		if hdr == "" {
			respondJSON(w, http.StatusUnauthorized, errBody("missing Authorization header"))
			return
		}
		plaintext, ok := strings.CutPrefix(hdr, "Bearer ")
		if !ok {
			respondJSON(w, http.StatusUnauthorized, errBody("Authorization must be Bearer sk_..."))
			return
		}

		key, err := auth.Verify(s.st.Keys(), plaintext)
		if err != nil {
			respondJSON(w, http.StatusUnauthorized, errBody("invalid or revoked key"))
			return
		}

		if requireAdmin && key.Role != auth.RoleAdmin {
			respondJSON(w, http.StatusForbidden, errBody("admin role required"))
			return
		}

		// Store authenticated key and tenant scope on context (P3).
		ctx := context.WithValue(r.Context(), authKeyCtxKey{}, key)
		scope := identity.Scope{Tenant: key.TenantID}
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
