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
//   - Tenant is required.
//   - Lower levels are optional but contiguous: Session requires User; User
//     requires Project. (No "holes" in the scope chain.)
type Scope struct {
	Tenant  string
	Project string
	User    string
	Session string
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

// Validate checks scope constraints.
// Returns ErrInvalidScope (wrapped) on failure.
func (s Scope) Validate() error {
	if s.Tenant == "" {
		return fmt.Errorf("%w: tenant is required", ErrInvalidScope)
	}
	if s.Session != "" && s.User == "" {
		return fmt.Errorf("%w: session requires user", ErrInvalidScope)
	}
	if s.User != "" && s.Project == "" {
		return fmt.Errorf("%w: user requires project", ErrInvalidScope)
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
