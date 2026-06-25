package identity_test

import (
	"context"
	"errors"
	"testing"

	"github.com/hurtener/stowage/internal/identity"
)

// TestScopeString verifies the canonical string form.
var scopeStringTests = []struct {
	scope identity.Scope
	want  string
}{
	{identity.Scope{Tenant: "acme"}, "acme"},
	{identity.Scope{Tenant: "acme", Project: "p1"}, "acme/p1"},
	{identity.Scope{Tenant: "acme", Project: "p1", User: "u1"}, "acme/p1/u1"},
	{identity.Scope{Tenant: "acme", Project: "p1", User: "u1", Session: "s1"}, "acme/p1/u1/s1"},
	// Project set but empty User and Session: only tenant/project.
	{identity.Scope{Tenant: "t", Project: "p", User: "", Session: ""}, "t/p"},
}

func TestScopeString(t *testing.T) {
	for _, tt := range scopeStringTests {
		got := tt.scope.String()
		if got != tt.want {
			t.Errorf("Scope%+v.String() = %q, want %q", tt.scope, got, tt.want)
		}
	}
}

// TestValidateMatrix covers the scope rules: only Tenant is required; project/user/
// session are INDEPENDENT optional dimensions (Phase 30 B4 — no contiguity).
var validateTests = []struct {
	scope   identity.Scope
	wantErr bool
	desc    string
}{
	{identity.Scope{Tenant: "acme"}, false, "tenant only — valid"},
	{identity.Scope{Tenant: "acme", Project: "p1"}, false, "tenant+project — valid"},
	{identity.Scope{Tenant: "acme", Project: "p1", User: "u1"}, false, "tenant+project+user — valid"},
	{identity.Scope{Tenant: "acme", Project: "p1", User: "u1", Session: "s1"}, false, "all fields — valid"},
	{identity.Scope{}, true, "empty tenant — invalid"},
	// Independent dimensions: each of these is now VALID (D-125 multi-user-no-projects, etc.).
	{identity.Scope{Tenant: "acme", User: "u1"}, false, "tenant+user, no project — valid (D-125)"},
	{identity.Scope{Tenant: "acme", Session: "s1"}, false, "tenant+session, no user — valid"},
	{identity.Scope{Tenant: "acme", Session: "s1", User: "u1"}, false, "tenant+user+session, no project — valid"},
}

func TestValidate(t *testing.T) {
	for _, tt := range validateTests {
		tt := tt
		t.Run(tt.desc, func(t *testing.T) {
			err := tt.scope.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr = %v", err, tt.wantErr)
			}
			if tt.wantErr && err != nil {
				if !errors.Is(err, identity.ErrInvalidScope) {
					t.Errorf("error %v does not wrap ErrInvalidScope", err)
				}
			}
		})
	}
}

// TestContextRoundTrip verifies WithScope / FromContext (AC-5).
func TestContextRoundTrip(t *testing.T) {
	want := identity.Scope{Tenant: "acme", Project: "proj", User: "bob"}
	ctx := identity.WithScope(context.Background(), want)
	got, err := identity.FromContext(ctx)
	if err != nil {
		t.Fatalf("FromContext: %v", err)
	}
	if got != want {
		t.Errorf("FromContext() = %+v, want %+v", got, want)
	}
}

// TestContextMissing verifies ErrScopeMissing on empty context.
func TestContextMissing(t *testing.T) {
	_, err := identity.FromContext(context.Background())
	if !errors.Is(err, identity.ErrScopeMissing) {
		t.Errorf("FromContext(empty) = %v, want ErrScopeMissing", err)
	}
}

// TestContextIsolation verifies WithScope doesn't affect the parent context.
func TestContextIsolation(t *testing.T) {
	parent := context.Background()
	child := identity.WithScope(parent, identity.Scope{Tenant: "t"})

	if _, err := identity.FromContext(parent); !errors.Is(err, identity.ErrScopeMissing) {
		t.Error("parent context should not have scope after WithScope on child")
	}
	if _, err := identity.FromContext(child); err != nil {
		t.Errorf("child context FromContext error: %v", err)
	}
}

// TestContextOverwrite verifies inner scope shadows outer scope.
func TestContextOverwrite(t *testing.T) {
	outer := identity.Scope{Tenant: "outer"}
	inner := identity.Scope{Tenant: "inner", Project: "p"}

	ctx := identity.WithScope(context.Background(), outer)
	ctx = identity.WithScope(ctx, inner)

	got, err := identity.FromContext(ctx)
	if err != nil {
		t.Fatalf("FromContext: %v", err)
	}
	if got != inner {
		t.Errorf("got %+v, want inner scope %+v", got, inner)
	}
}
