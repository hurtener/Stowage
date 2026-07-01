package mcpserver

// Unit tests for makeBrowseHandler / memoryToBrowseItem (memory_browse, ae5,
// D-143). Mirrors the HTTP twin (internal/api/browse_handler_test.go):
// recent-default, superseded mode, unknown-mode rejection, bad-cursor
// rejection, limit/pagination, and project/user scope narrowing (D-125).
// The cross-package integration coverage doesn't count toward this package's
// self-coverage band (AGENTS.md §11) — these tests close that gap.

import (
	"context"
	"testing"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/store"
)

func seedBrowseHandlerMemories(t *testing.T, svc *Services, scope identity.Scope) {
	t.Helper()
	ctx := context.Background()
	mk := func(id, status string, createdAt int64) {
		if err := svc.Store.Memories().Insert(ctx, scope, store.Memory{
			ID: id, Kind: "fact", Content: "content-" + id, Status: status,
			Confidence: 0.5, TrustSource: "llm_extracted", Stability: 1.0,
			CreatedAt: createdAt, UpdatedAt: createdAt,
		}); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	mk("01MBRWM1AAAAAAAAAAAAAAAAAA", "active", 1000)
	mk("01MBRWM2AAAAAAAAAAAAAAAAAA", "active", 2000)
	mk("01MBRWM3AAAAAAAAAAAAAAAAAA", "superseded", 3000)
}

// TestHandlerBrowse_RecentDefault proves memory_browse (default mode=recent)
// returns the scope's memories most-recent-first, status-agnostic, and that
// memoryToBrowseItem maps the Structured fields (ae5, D-143).
func TestHandlerBrowse_RecentDefault(t *testing.T) {
	svc := newHandlerServices(t)
	h := makeBrowseHandler(svc)
	scope := testScope()
	seedBrowseHandlerMemories(t, svc, scope)

	res, err := h(context.Background(), BrowseInput{})
	if err != nil {
		t.Fatalf("browse: %v", err)
	}
	if len(res.Structured.Memories) != 3 {
		t.Fatalf("want 3 memories, got %d: %+v", len(res.Structured.Memories), res.Structured.Memories)
	}
	got := res.Structured.Memories
	if got[0].ID != "01MBRWM3AAAAAAAAAAAAAAAAAA" || got[1].ID != "01MBRWM2AAAAAAAAAAAAAAAAAA" || got[2].ID != "01MBRWM1AAAAAAAAAAAAAAAAAA" {
		t.Errorf("expected most-recent-first order, got %+v", got)
	}
	// Assert the memoryToBrowseItem mapping populated the Structured item.
	first := got[0]
	if first.Kind != "fact" || first.Content != "content-01MBRWM3AAAAAAAAAAAAAAAAAA" ||
		first.Status != "superseded" || first.TrustSource != "llm_extracted" ||
		first.Confidence != 0.5 || first.Stability != 1.0 || first.CreatedAt != 3000 || first.UpdatedAt != 3000 {
		t.Errorf("memoryToBrowseItem mapping wrong: %+v", first)
	}
	if res.Structured.NextCursor != "" {
		t.Errorf("expected no next_cursor on a full page, got %q", res.Structured.NextCursor)
	}
	if res.Text == "" {
		t.Error("Text must not be empty")
	}
}

// TestHandlerBrowse_SupersededMode proves mode=superseded returns only
// superseded rows, oldest-first (the deliberate H4 asymmetry).
func TestHandlerBrowse_SupersededMode(t *testing.T) {
	svc := newHandlerServices(t)
	h := makeBrowseHandler(svc)
	scope := testScope()
	seedBrowseHandlerMemories(t, svc, scope)
	// A second superseded row so ordering is non-trivial.
	if err := svc.Store.Memories().Insert(context.Background(), scope, store.Memory{
		ID: "01MBRWM0AAAAAAAAAAAAAAAAAA", Kind: "fact", Content: "content-m0", Status: "superseded",
		Confidence: 0.5, TrustSource: "llm_extracted", Stability: 1.0, CreatedAt: 500, UpdatedAt: 500,
	}); err != nil {
		t.Fatalf("seed m0: %v", err)
	}

	res, err := h(context.Background(), BrowseInput{Mode: "superseded"})
	if err != nil {
		t.Fatalf("browse superseded: %v", err)
	}
	got := res.Structured.Memories
	if len(got) != 2 {
		t.Fatalf("want 2 memories, got %d: %+v", len(got), got)
	}
	if got[0].ID != "01MBRWM0AAAAAAAAAAAAAAAAAA" || got[1].ID != "01MBRWM3AAAAAAAAAAAAAAAAAA" {
		t.Errorf("expected oldest-first order, got %+v", got)
	}
	for _, m := range got {
		if m.Status != "superseded" {
			t.Errorf("non-superseded row in superseded mode: %+v", m)
		}
	}
}

// TestHandlerBrowse_UnknownMode proves an unrecognised mode is rejected,
// never silently defaulted (AC-7).
func TestHandlerBrowse_UnknownMode(t *testing.T) {
	svc := newHandlerServices(t)
	h := makeBrowseHandler(svc)

	if _, err := h(context.Background(), BrowseInput{Mode: "bogus"}); err == nil {
		t.Error("expected an error for an unknown mode")
	}
}

// TestHandlerBrowse_BadCursor proves a malformed cursor is rejected, not a
// panic or a silent first page (AC-8).
func TestHandlerBrowse_BadCursor(t *testing.T) {
	svc := newHandlerServices(t)
	h := makeBrowseHandler(svc)
	scope := testScope()
	seedBrowseHandlerMemories(t, svc, scope)

	if _, err := h(context.Background(), BrowseInput{Cursor: "not-a-cursor"}); err == nil {
		t.Error("expected an error for a malformed cursor")
	}
}

// TestHandlerBrowse_LimitAndPagination proves an explicit limit paginates via
// NextCursor.
func TestHandlerBrowse_LimitAndPagination(t *testing.T) {
	svc := newHandlerServices(t)
	h := makeBrowseHandler(svc)
	scope := testScope()
	seedBrowseHandlerMemories(t, svc, scope)

	res, err := h(context.Background(), BrowseInput{Limit: 2})
	if err != nil {
		t.Fatalf("browse limit=2: %v", err)
	}
	if len(res.Structured.Memories) != 2 {
		t.Fatalf("want 2 memories, got %d", len(res.Structured.Memories))
	}
	if res.Structured.NextCursor == "" {
		t.Fatal("expected a next_cursor — a third row remains")
	}

	res2, err := h(context.Background(), BrowseInput{Limit: 2, Cursor: res.Structured.NextCursor})
	if err != nil {
		t.Fatalf("browse page2: %v", err)
	}
	if len(res2.Structured.Memories) != 1 {
		t.Fatalf("page2: want 1 memory, got %d", len(res2.Structured.Memories))
	}
	if res2.Structured.NextCursor != "" {
		t.Errorf("expected an empty cursor on the last page, got %q", res2.Structured.NextCursor)
	}
}

// TestHandlerBrowse_ProjectUserScopeNarrowing proves ProjectID/UserID narrow
// the walk to a sub-tenant identity (P3, D-125): a memory scoped to a
// different project/user is invisible to a narrower browse.
func TestHandlerBrowse_ProjectUserScopeNarrowing(t *testing.T) {
	svc := newHandlerServices(t)
	h := makeBrowseHandler(svc)
	tenantScope := testScope()
	projectScope := identity.Scope{Tenant: tenantScope.Tenant, Project: "proj-a"}
	userScope := identity.Scope{Tenant: tenantScope.Tenant, Project: "proj-a", User: "user-a"}

	ctx := context.Background()
	if err := svc.Store.Memories().Insert(ctx, projectScope, store.Memory{
		ID: "01MBRWPROJAAAAAAAAAAAAAAA", Kind: "fact", Content: "project-scoped", Status: "active",
		Confidence: 0.5, TrustSource: "llm_extracted", Stability: 1.0, CreatedAt: 100, UpdatedAt: 100,
	}); err != nil {
		t.Fatalf("seed project memory: %v", err)
	}
	if err := svc.Store.Memories().Insert(ctx, userScope, store.Memory{
		ID: "01MBRWUSERAAAAAAAAAAAAAAA", Kind: "fact", Content: "user-scoped", Status: "active",
		Confidence: 0.5, TrustSource: "llm_extracted", Stability: 1.0, CreatedAt: 200, UpdatedAt: 200,
	}); err != nil {
		t.Fatalf("seed user memory: %v", err)
	}

	// Narrowing to proj-a alone (no user) is hierarchical — it sees every
	// memory scoped under proj-a, including the user-scoped one.
	res, err := h(ctx, BrowseInput{ProjectID: "proj-a"})
	if err != nil {
		t.Fatalf("browse project scope: %v", err)
	}
	if len(res.Structured.Memories) != 2 {
		t.Fatalf("expected both proj-a memories, got %+v", res.Structured.Memories)
	}

	// Narrowing to proj-a + user-a must see only the user-scoped memory —
	// the project-scoped-only memory (User="") is a sibling, not an ancestor,
	// so it stays invisible to the narrower walk.
	res2, err := h(ctx, BrowseInput{ProjectID: "proj-a", UserID: "user-a"})
	if err != nil {
		t.Fatalf("browse user scope: %v", err)
	}
	if len(res2.Structured.Memories) != 1 || res2.Structured.Memories[0].ID != "01MBRWUSERAAAAAAAAAAAAAAA" {
		t.Fatalf("expected only the user-scoped memory, got %+v", res2.Structured.Memories)
	}

	// A different project must not see proj-a's memories at all.
	res3, err := h(ctx, BrowseInput{ProjectID: "proj-b"})
	if err != nil {
		t.Fatalf("browse other project scope: %v", err)
	}
	for _, m := range res3.Structured.Memories {
		if m.ID == "01MBRWPROJAAAAAAAAAAAAAAA" || m.ID == "01MBRWUSERAAAAAAAAAAAAAAA" {
			t.Errorf("proj-b browse leaked a proj-a-scoped memory: %+v", m)
		}
	}
}
