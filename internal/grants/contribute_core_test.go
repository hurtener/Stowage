package grants_test

// contribute_core_test.go — D-071: the shared contribute core
// (Service.AuthorizeContribute + ContributeContext.ApplyTo) used by BOTH the HTTP
// records handler and the MCP memory_ingest handler.

import (
	"context"
	"errors"
	"testing"

	"github.com/hurtener/stowage/internal/grants"
	"github.com/hurtener/stowage/internal/identity"
)

func TestAuthorizeContribute_CoveredReturnsOverride(t *testing.T) {
	st := newMockGrantStore()
	svc := grants.New(st, nil, noopLog())
	ctx := context.Background()
	scope := identity.Scope{Tenant: "t1"}

	g, err := svc.CreateGroup(ctx, scope, "writers")
	if err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	if _, err := svc.AddMember(ctx, scope, g.ID, "alice"); err != nil {
		t.Fatalf("AddMember: %v", err)
	}
	if _, err := svc.CreateGrant(ctx, scope, grants.CreateGrantInput{
		OwnerScope:  identity.Scope{Tenant: "t1", User: "bob"},
		GroupID:     g.ID,
		Access:      "contribute",
		ZoneCeiling: "work",
	}); err != nil {
		t.Fatalf("CreateGrant: %v", err)
	}

	cc, err := svc.AuthorizeContribute(ctx, scope,
		identity.Scope{Tenant: "t1", User: "bob", Session: "sess-x"}, "alice")
	if err != nil {
		t.Fatalf("AuthorizeContribute covered: %v", err)
	}
	if cc.TargetUser != "bob" || cc.TargetSession != "sess-x" {
		t.Errorf("override context wrong: %+v", cc)
	}
}

func TestAuthorizeContribute_NotCovered(t *testing.T) {
	st := newMockGrantStore()
	svc := grants.New(st, nil, noopLog())
	ctx := context.Background()
	scope := identity.Scope{Tenant: "t1"}

	_, err := svc.AuthorizeContribute(ctx, scope,
		identity.Scope{Tenant: "t1", User: "carol"}, "mallory")
	if !errors.Is(err, grants.ErrNotCovered) {
		t.Errorf("AuthorizeContribute not covered: want ErrNotCovered, got %v", err)
	}
}

func TestAuthorizeContribute_CrossTenant(t *testing.T) {
	st := newMockGrantStore()
	svc := grants.New(st, nil, noopLog())
	ctx := context.Background()

	_, err := svc.AuthorizeContribute(ctx,
		identity.Scope{Tenant: "t1"},
		identity.Scope{Tenant: "t2", User: "bob"}, "alice")
	if !errors.Is(err, grants.ErrCrossTenantGrant) {
		t.Errorf("AuthorizeContribute cross-tenant: want ErrCrossTenantGrant, got %v", err)
	}
}

func TestContributeContextApplyTo(t *testing.T) {
	cc := grants.ContributeContext{TargetUser: "bob"} // only user overridden
	p, u, s := cc.ApplyTo("proj-keep", "alice", "sess-keep")
	if p != "proj-keep" || u != "bob" || s != "sess-keep" {
		t.Errorf("ApplyTo override wrong: got (%q,%q,%q)", p, u, s)
	}

	full := grants.ContributeContext{TargetProject: "P", TargetUser: "U", TargetSession: "S"}
	p, u, s = full.ApplyTo("a", "b", "c")
	if p != "P" || u != "U" || s != "S" {
		t.Errorf("ApplyTo full override wrong: got (%q,%q,%q)", p, u, s)
	}
}
