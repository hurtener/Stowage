package mcpserver

// contribute_test.go — AC-3 (D-071): contribute-mode is honored on the MCP
// memory_ingest surface via the shared grants.AuthorizeContribute core. With a
// covering contribute grant the records are stamped with the pool-owner's scope
// (observable on the enqueued pipeline Item); without one the request is rejected
// (h2's fail-loud is replaced, never a silent mis-scope). Uses a real sqlite
// store + a real grants.Service.

import (
	"context"
	"testing"

	"github.com/hurtener/stowage/internal/grants"
	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/pipeline"
	"github.com/hurtener/stowage/internal/store"
)

func newContributeServices(t *testing.T) (*Services, store.Store, chan pipeline.Item) {
	t.Helper()
	st := newHandlerStore(t)
	log := noopLog()
	ch := make(chan pipeline.Item, 8)
	return &Services{
		Store:      st,
		GrantsSvc:  grants.New(st.Grants(), st.Events(), log),
		PipelineIn: ch,
		Log:        log,
		ScopeFn:    StdioScopeFn("contrib-tenant"),
	}, st, ch
}

func TestIngestContributeHonored(t *testing.T) {
	const (
		tenant      = "contrib-tenant"
		contributor = "alice"
		poolOwner   = "bob"
		poolSession = "pool-session"
	)
	svc, _, ch := newContributeServices(t)
	ctx := context.Background()
	tenantScope := identity.Scope{Tenant: tenant}

	// Build a group, add the contributor, and grant the group contribute access
	// to the pool-owner's scope.
	g, err := svc.GrantsSvc.CreateGroup(ctx, tenantScope, "team")
	if err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	if _, err := svc.GrantsSvc.AddMember(ctx, tenantScope, g.ID, contributor); err != nil {
		t.Fatalf("AddMember: %v", err)
	}
	if _, err := svc.GrantsSvc.CreateGrant(ctx, tenantScope, grants.CreateGrantInput{
		OwnerScope:  identity.Scope{Tenant: tenant, User: poolOwner},
		GroupID:     g.ID,
		Access:      "contribute",
		ZoneCeiling: "work",
	}); err != nil {
		t.Fatalf("CreateGrant: %v", err)
	}

	h := makeIngestHandler(svc)

	// With a covering grant: the record is stamped with the pool-owner scope —
	// observable as the overridden session on the enqueued pipeline Item.
	res, err := h(ctx, IngestInput{
		Records:           []IngestRecord{{Role: "user", Content: "shared team fact"}},
		TargetScope:       &IngestTargetScope{UserID: poolOwner, SessionID: poolSession},
		ContributorUserID: contributor,
	})
	if err != nil {
		t.Fatalf("contribute ingest with grant: unexpected error: %v", err)
	}
	if len(res.Structured.IDs) != 1 || !res.Structured.Enqueued {
		t.Fatalf("contribute ingest: unexpected result %+v", res.Structured)
	}
	select {
	case item := <-ch:
		if item.SessionID != poolSession {
			t.Errorf("contribute write mis-scoped: enqueued Item session=%q want %q (pool-owner override)", item.SessionID, poolSession)
		}
	default:
		t.Fatal("contribute ingest: no item enqueued")
	}
}

func TestIngestContributeRejectedWithoutGrant(t *testing.T) {
	svc, _, _ := newContributeServices(t)
	ctx := context.Background()
	h := makeIngestHandler(svc)

	// No group/grant for the contributor: the request must be rejected, not
	// silently mis-scoped into the caller's own pool.
	_, err := h(ctx, IngestInput{
		Records:           []IngestRecord{{Role: "user", Content: "should be rejected"}},
		TargetScope:       &IngestTargetScope{UserID: "carol"},
		ContributorUserID: "mallory",
	})
	if err == nil {
		t.Fatal("contribute ingest without a grant: expected rejection, got nil")
	}
}

func TestIngestContributeNilGrantsService(t *testing.T) {
	svc, _, _ := newContributeServices(t)
	svc.GrantsSvc = nil
	ctx := context.Background()
	h := makeIngestHandler(svc)
	_, err := h(ctx, IngestInput{
		Records:     []IngestRecord{{Role: "user", Content: "x"}},
		TargetScope: &IngestTargetScope{UserID: "bob"},
	})
	if err == nil {
		t.Fatal("contribute ingest with nil grants service: expected error, got nil")
	}
}
