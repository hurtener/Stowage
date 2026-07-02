package identity_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
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

// TestTenantMismatchSentinel verifies ErrTenantMismatch (ae2, D-138) is a
// distinct, value-free sentinel: its message contains no tenant value (a
// literal-free assertion) and it is errors.Is-matchable through a %w wrap, the
// exact pattern mcpserver's readMetaIdentity uses.
func TestTenantMismatchSentinel(t *testing.T) {
	if identity.ErrTenantMismatch == nil {
		t.Fatal("ErrTenantMismatch must not be nil")
	}
	if errors.Is(identity.ErrTenantMismatch, identity.ErrInvalidScope) {
		t.Error("ErrTenantMismatch must be distinct from ErrInvalidScope")
	}
	if errors.Is(identity.ErrTenantMismatch, identity.ErrScopeMissing) {
		t.Error("ErrTenantMismatch must be distinct from ErrScopeMissing")
	}

	wrapped := fmt.Errorf("mcpserver: %w", identity.ErrTenantMismatch)
	if !errors.Is(wrapped, identity.ErrTenantMismatch) {
		t.Error("a %w-wrapped ErrTenantMismatch must remain errors.Is-matchable")
	}

	// Literal-free: neither a would-be injected nor a would-be real tenant value
	// appears anywhere in the sentinel's message.
	msg := identity.ErrTenantMismatch.Error()
	for _, leak := range []string{"acme", "credTenant", "tenant-a", "tenant-b"} {
		if strings.Contains(msg, leak) {
			t.Errorf("ErrTenantMismatch message %q must not contain a tenant value %q", msg, leak)
		}
	}
}

// TestResolveViewSubject covers the ae9 (D-149) read-time topic-VIEW subject
// resolver: agent-only, key-only, both-with-precedence in both orders, and
// none→unbound. A pure function — no I/O, no Retriever needed.
func TestResolveViewSubject(t *testing.T) {
	cases := []struct {
		name       string
		agentID    string
		keyID      string
		precedence string
		wantKind   string
		wantID     string
		wantOK     bool
	}{
		{"agent only", "agent-1", "", "agent,key", "agent", "agent-1", true},
		{"key only", "", "sk_1", "agent,key", "key", "sk_1", true},
		{"neither → unbound", "", "", "agent,key", "", "", false},
		{"both, default precedence agent,key → agent wins", "agent-1", "sk_1", "agent,key", "agent", "agent-1", true},
		{"both, flipped precedence key,agent → key wins", "agent-1", "sk_1", "key,agent", "key", "sk_1", true},
		{"both, empty precedence falls back to agent,key default", "agent-1", "sk_1", "", "agent", "agent-1", true},
		{"both, unrecognized precedence falls back to agent,key default", "agent-1", "sk_1", "bogus", "agent", "agent-1", true},
		{"key only, unrecognized precedence still resolves key (only one present)", "", "sk_1", "bogus", "key", "sk_1", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kind, id, ok := identity.ResolveViewSubject(tc.agentID, tc.keyID, tc.precedence)
			if kind != tc.wantKind || id != tc.wantID || ok != tc.wantOK {
				t.Errorf("ResolveViewSubject(%q,%q,%q) = (%q,%q,%v), want (%q,%q,%v)",
					tc.agentID, tc.keyID, tc.precedence, kind, id, ok, tc.wantKind, tc.wantID, tc.wantOK)
			}
		})
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
