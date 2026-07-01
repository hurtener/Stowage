// Package identity defines the Scope type and its context plumbing.
// Scope is the identity container that enforces P3 (scopes at write and read).
// No store coupling lives here.
package identity

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// ErrScopeMissing is returned when context has no Scope.
var ErrScopeMissing = errors.New("identity: scope missing from context")

// ErrInvalidScope is returned when Validate fails.
var ErrInvalidScope = errors.New("identity: invalid scope")

// Scope identifies the tenant, project, user, and session for an operation.
//
// Constraints (enforced by Validate):
//   - Tenant is required (the auth boundary).
//   - Project, User, and Session are INDEPENDENT optional dimensions — any
//     combination is valid (e.g. {Tenant, User} with no Project is a legitimate
//     multi-user-no-projects deployment, D-125). This matches the store's
//     buildScopeWhere, which filters each set dimension independently. (Earlier
//     versions required a contiguous chain; that contradicted read scoping and
//     was relaxed in Phase 30, B4.)
type Scope struct {
	Tenant  string
	Project string
	User    string
	Session string
	// Agent is the calling agent identity, set ONLY on the read path (from
	// _meta.agent_id on MCP, or an explicit agent_id field on HTTP/SDK). It is a
	// READ-TIME identity/filter dimension (D-135): it is NEVER persisted, NEVER a
	// column on any of the 12 scope tables, and NEVER referenced by a scope-WHERE
	// builder or an INSERT. It drives only the read-time agent→topic filter
	// (internal/retrieval), which can only SUBTRACT from the caller's own-scope
	// results (P3 preserved, fails open per D-139).
	Agent string
}

// String returns the canonical slash-separated form, omitting empty trailing
// components.
//
// Examples:
//
//	Scope{Tenant:"acme"}                              → "acme"
//	Scope{Tenant:"acme", Project:"p1"}                → "acme/p1"
//	Scope{Tenant:"acme", Project:"p1", User:"u1"}     → "acme/p1/u1"
//	Scope{Tenant:"acme", Project:"p1", User:"u1", Session:"s1"} → "acme/p1/u1/s1"
func (s Scope) String() string {
	parts := []string{s.Tenant}
	if s.Project != "" {
		parts = append(parts, s.Project)
		if s.User != "" {
			parts = append(parts, s.User)
			if s.Session != "" {
				parts = append(parts, s.Session)
			}
		}
	}
	return strings.Join(parts, "/")
}

// Validate checks scope constraints. Returns ErrInvalidScope (wrapped) on failure.
//
// Only Tenant is required. Project/User/Session are independent optional
// dimensions (Phase 30, B4): the store filters each one independently via
// buildScopeWhere, and a per-user read with no project ({Tenant, User}) is a
// first-class shape (D-125). There is no contiguity requirement.
func (s Scope) Validate() error {
	if s.Tenant == "" {
		return fmt.Errorf("%w: tenant is required", ErrInvalidScope)
	}
	return nil
}

type contextKey struct{}

// WithScope returns a new context carrying s.
func WithScope(ctx context.Context, s Scope) context.Context {
	return context.WithValue(ctx, contextKey{}, s)
}

// FromContext retrieves the Scope from ctx.
// Returns ErrScopeMissing if no scope has been stored.
func FromContext(ctx context.Context) (Scope, error) {
	s, ok := ctx.Value(contextKey{}).(Scope)
	if !ok {
		return Scope{}, ErrScopeMissing
	}
	return s, nil
}
