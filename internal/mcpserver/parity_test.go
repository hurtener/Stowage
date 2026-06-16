package mcpserver

// parity_test.go — Wave-B checkpoint META-FIX: extend surface-parity coverage to
// the MCP write verbs. The h3/h4 parity suites only compared embedded↔HTTP, so
// the MCP-surface cache-invalidation drift (rollback/resolve/assert skipped the
// D-053 InvalidateScope that HTTP/SDK performed) and the MCP topic-validation
// gap (active|paused not enforced inline) went uncaught. These tests are the ones
// that would have caught FAIL-1..3:
//
//   - after each MCP content-changing write (rollback, resolve-confirm, assert),
//     a same-scope memory_retrieve is no longer served from the stale cache
//     (CacheHit flips back to false) — proving the write busted the cache; and
//   - memory_topics upsert rejects an invalid status on the MCP surface.
//
// The result cache only stores a query that returned ≥1 item (the retriever
// short-circuits an empty result before the cache Put), so every probe is backed
// by an active anchor memory whose content matches the probe query.

import (
	"context"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/hurtener/stowage/internal/reconcile"
	"github.com/hurtener/stowage/internal/store"
)

// (seedActiveMemory is defined in handlers_reversibility_test.go and reused here.)

// retrieveCacheHit runs memory_retrieve for query and returns its CacheHit flag.
func retrieveCacheHit(t *testing.T, svc *Services, query string) bool {
	t.Helper()
	h := makeRetrieveHandler(svc)
	res, err := h(context.Background(), RetrieveInput{Query: query, Limit: 5})
	if err != nil {
		t.Fatalf("memory_retrieve: %v", err)
	}
	return res.Structured.CacheHit
}

// assertCacheBustedBy primes the result cache for query (first call misses +
// populates, second call hits), runs write, and asserts the same-scope retrieve
// is no longer a cache hit — i.e. write invalidated the scope (D-053).
func assertCacheBustedBy(t *testing.T, svc *Services, query string, write func(t *testing.T)) {
	t.Helper()
	if got := retrieveCacheHit(t, svc, query); got {
		t.Fatalf("priming retrieve unexpectedly a cache hit on first call")
	}
	if got := retrieveCacheHit(t, svc, query); !got {
		t.Fatalf("second retrieve not a cache hit; cache not populated as expected")
	}
	write(t)
	if got := retrieveCacheHit(t, svc, query); got {
		t.Fatalf("retrieve still served from cache after MCP write; scope was NOT invalidated (D-053 violation)")
	}
}

// TestMCPParity_AssertInvalidatesCache proves memory_assert busts the cache.
func TestMCPParity_AssertInvalidatesCache(t *testing.T) {
	svc := newFullServices(t)
	const probe = "assertcacheprobe anchor"
	seedActiveMemory(t, svc.Store, testScope(), probe)

	assertCacheBustedBy(t, svc, probe, func(t *testing.T) {
		t.Helper()
		h := makeAssertHandler(svc)
		if _, err := h(context.Background(), AssertInput{
			Action: "add", Content: "mcp asserted fact", Kind: "fact",
		}); err != nil {
			t.Fatalf("memory_assert add: %v", err)
		}
	})
}

// TestMCPParity_RollbackInvalidatesCache proves memory_rollback busts the cache.
func TestMCPParity_RollbackInvalidatesCache(t *testing.T) {
	svc := newFullServices(t)
	scope := testScope()
	ctx := context.Background()
	now := time.Now().UnixMilli()
	const probe = "rollbackcacheprobe mutated content"

	memID := ulid.Make().String()
	mem := store.Memory{
		ID: memID, TenantID: scope.Tenant, Kind: "fact", Content: "rollbackcacheprobe original content",
		Status: "active", Importance: 3, Confidence: 0.8, TrustSource: "llm_extracted",
		Stability: 1.0, ContentHash: ulid.Make().String(), CreatedAt: now, UpdatedAt: now,
	}
	if err := svc.Store.Memories().Commit(ctx, scope, store.CommitSet{
		Action: store.ActionAdd, Memory: mem, Entities: []string{"e"}, Keywords: []string{"k"},
		Events: []store.Event{{ID: ulid.Make().String(), Type: "memory.added", SubjectID: memID, Payload: "{}", CreatedAt: now}},
		Scope:  scope,
	}); err != nil {
		t.Fatalf("seed memory: %v", err)
	}
	jt, _ := svc.Store.Memories().GetJunctions(ctx, scope, memID)
	if err := svc.Store.Events().Emit(ctx, scope, store.Event{
		ID: ulid.Make().String(), Type: "memory.updated", SubjectID: memID,
		Payload: reconcile.MarshalPriorState(mem, jt), CreatedAt: time.Now().UnixMilli(),
	}); err != nil {
		t.Fatalf("emit updated: %v", err)
	}
	// Mutate so the memory's content matches the probe and the cache populates;
	// the rollback (content-changing commit) then invalidates the scope.
	upd := mem
	upd.Content = probe
	upd.UpdatedAt = time.Now().UnixMilli()
	if err := svc.Store.Memories().Update(ctx, scope, upd); err != nil {
		t.Fatalf("mutate memory: %v", err)
	}

	assertCacheBustedBy(t, svc, probe, func(t *testing.T) {
		t.Helper()
		h := makeRollbackHandler(svc)
		res, err := h(ctx, RollbackInput{MemoryID: memID})
		if err != nil {
			t.Fatalf("memory_rollback: %v", err)
		}
		if res.Structured.Memory.ID != memID {
			t.Fatalf("rollback returned wrong memory: %+v", res.Structured.Memory)
		}
	})
}

// TestMCPParity_ResolveConfirmInvalidatesCache proves memory_resolve (confirm)
// busts the cache. An active anchor backs the probe (the parked memory itself is
// not retrievable until confirmed).
func TestMCPParity_ResolveConfirmInvalidatesCache(t *testing.T) {
	svc := newFullServices(t)
	scope := testScope()
	ctx := context.Background()
	now := time.Now().UnixMilli()
	const probe = "resolvecacheprobe anchor"
	seedActiveMemory(t, svc.Store, scope, probe)

	parkedID := ulid.Make().String()
	parked := store.Memory{
		ID: parkedID, TenantID: scope.Tenant, Kind: "fact", Content: "parked mcp memory",
		Status: "pending_confirmation", Importance: 3, Confidence: 0.8,
		TrustSource: "llm_extracted", Stability: 1.0, ContentHash: ulid.Make().String(),
		CreatedAt: now, UpdatedAt: now,
	}
	if err := svc.Store.Memories().Insert(ctx, scope, parked); err != nil {
		t.Fatalf("insert parked: %v", err)
	}

	assertCacheBustedBy(t, svc, probe, func(t *testing.T) {
		t.Helper()
		h := makeResolveHandler(svc)
		res, err := h(ctx, ResolveInput{MemoryID: parkedID, Action: "confirm"})
		if err != nil {
			t.Fatalf("memory_resolve confirm: %v", err)
		}
		if res.Structured.Status != "active" {
			t.Fatalf("resolve confirm status: got %q want active", res.Structured.Status)
		}
	})
}

// TestMCPParity_TopicUpsertRejectsBadStatus proves the MCP memory_topics surface
// enforces the active|paused validation (the FAIL-3 gap: the prior inline build
// accepted any status). The shared topics.Service now backs all surfaces.
func TestMCPParity_TopicUpsertRejectsBadStatus(t *testing.T) {
	svc := newHandlerServices(t) // includes a real topics.Service
	h := makeTopicsHandler(svc)
	ctx := context.Background()

	if _, err := h(ctx, TopicsInput{
		Action: "upsert",
		Topics: []TopicItem{{Key: "k", Status: "bogus"}},
	}); err == nil {
		t.Fatal("memory_topics upsert with invalid status: expected rejection, got nil")
	}

	// A valid status still succeeds (the validation is not over-broad).
	if _, err := h(ctx, TopicsInput{
		Action: "upsert",
		Topics: []TopicItem{{Key: "k", Status: "paused"}},
	}); err != nil {
		t.Fatalf("memory_topics upsert with valid status: unexpected error: %v", err)
	}
}
