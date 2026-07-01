package retrieval_test

// cache_test.go — §6 cache-coherence coverage for the ae1 agent filter
// (D-135/D-146):
//
//   - blocking #1 (AC-12): scopeCacheKey/cacheKey key on Scope.Agent, so two
//     distinct bound agents never collide and a bound-agent read never returns an
//     earlier unbound/other-agent cached set.
//   - blocking #2 (AC-13): PutAgentPolicy/DeleteAgentPolicy invalidate affected
//     agent-filtered reads in the CORE (Retriever.PutAgentPolicy/DeleteAgentPolicy),
//     not the HTTP/MCP handlers — a direct store mutation that bypasses the core
//     wrapper must NOT invalidate, proving the invalidation genuinely lives there.

import (
	"context"
	"log/slog"
	"os"
	"testing"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/retrieval"
	"github.com/hurtener/stowage/internal/store"
	"github.com/hurtener/stowage/internal/vindex"
)

// --- blocking #1: cache keyed on Scope.Agent ----------------------------------

// TestCache_KeyedOnAgent_NoCollision proves two distinct bound agents with
// identical tenant/project/user/session/query/profile/window/kinds/lanes/limit
// get separate cache entries.
func TestCache_KeyedOnAgent_NoCollision(t *testing.T) {
	t.Parallel()
	c := retrieval.ExportNewResultCache(0)

	base := identity.Scope{Tenant: "t-cachekey-" + newID(), Project: "p1", User: "u1", Session: "s1"}
	agentA := base
	agentA.Agent = "agent-a"
	agentB := base
	agentB.Agent = "agent-b"

	itemsA := []retrieval.MemoryItem{{Memory: store.Memory{ID: "mA"}}}
	itemsB := []retrieval.MemoryItem{{Memory: store.Memory{ID: "mB"}}}

	c.Put(agentA, "sig", "balanced", "", 0, 0, nil, false, 10, itemsA, retrieval.Support{})
	c.Put(agentB, "sig", "balanced", "", 0, 0, nil, false, 10, itemsB, retrieval.Support{})

	gotA, _, ok := c.Get(agentA, "sig", "balanced", "", 0, 0, nil, false, 10)
	if !ok || len(gotA) != 1 || gotA[0].Memory.ID != "mA" {
		t.Fatalf("agentA cache: got %v ok=%v, want [mA] true", gotA, ok)
	}
	gotB, _, ok := c.Get(agentB, "sig", "balanced", "", 0, 0, nil, false, 10)
	if !ok || len(gotB) != 1 || gotB[0].Memory.ID != "mB" {
		t.Fatalf("agentB cache: got %v ok=%v, want [mB] true", gotB, ok)
	}
}

// TestCache_BoundAgentNeverServesUnboundCachedSet proves a bound-agent read never
// returns an earlier unbound/no-agent cached set for the same
// tenant/project/user/session/query/profile/window/kinds/lanes/limit — the
// post-fusion agent filter must never be silently skipped on a cache HIT.
func TestCache_BoundAgentNeverServesUnboundCachedSet(t *testing.T) {
	t.Parallel()
	c := retrieval.ExportNewResultCache(0)

	unbound := identity.Scope{Tenant: "t-cachekey2-" + newID(), User: "u1"}
	bound := unbound
	bound.Agent = "agent-x"

	itemsUnbound := []retrieval.MemoryItem{{Memory: store.Memory{ID: "unfiltered-item"}}}
	c.Put(unbound, "sig", "balanced", "", 0, 0, nil, false, 10, itemsUnbound, retrieval.Support{})

	// Same key dimensions except Agent — must be a cache MISS, never a hit on the
	// unbound entry.
	if _, _, ok := c.Get(bound, "sig", "balanced", "", 0, 0, nil, false, 10); ok {
		t.Error("a bound-agent read must not hit an unbound-agent cached entry")
	}
	// And the reverse: an unbound read must not hit a bound-agent's cached entry.
	itemsBound := []retrieval.MemoryItem{{Memory: store.Memory{ID: "agent-filtered-item"}}}
	c.Put(bound, "sig", "balanced", "", 0, 0, nil, false, 10, itemsBound, retrieval.Support{})
	if got, _, ok := c.Get(unbound, "sig", "balanced", "", 0, 0, nil, false, 10); ok && len(got) == 1 && got[0].Memory.ID == "agent-filtered-item" {
		t.Error("an unbound read must not hit a bound-agent's cached entry")
	}
}

// --- blocking #2: policy mutation invalidates the core's cached reads --------

// TestRetrieve_AgentPolicyMutation_InvalidatesCache proves the CORE
// Retriever.PutAgentPolicy invalidates the affected agent's cached reads
// immediately — no stale-until-TTL window (AC-13).
func TestRetrieve_AgentPolicyMutation_InvalidatesCache(t *testing.T) {
	t.Parallel()
	st := openStore(t)
	gw := openMockGateway(t, 4)
	vi := vindex.New(st.Vectors(), 4, "test")
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	r := retrieval.New(st.Memories(), st.Records(), vi, gw, log).WithAgentPolicy(st.TopicViews(), true)

	scope := identity.Scope{Tenant: "t-cacheinv-" + newID()}
	authID := commitMemoryFull(t, st, scope, "auth widget note qvzxe", "fact", nil, nil, nil, []string{"auth"})
	deployID := commitMemoryFull(t, st, scope, "deploy widget note qvzxe", "fact", nil, nil, nil, []string{"deploy"})

	readScope := scope
	readScope.Agent = "bound-agent"
	req := retrieval.Request{Query: "qvzxe widget", Limit: 10}

	// Before any binding: unbound, unfiltered — also primes the cache for this key.
	resp1, err := r.Retrieve(context.Background(), readScope, req)
	if err != nil {
		t.Fatalf("Retrieve (before bind): %v", err)
	}
	before := idSet(resp1)
	if !before[authID] || !before[deployID] {
		t.Fatalf("expected both items before binding, got %v", before)
	}

	// Bind via the CORE mutation path — must invalidate.
	if err := r.PutAgentPolicy(context.Background(), scope, store.AgentPolicy{
		AgentID: "bound-agent", AllowTopics: []string{"auth"},
	}); err != nil {
		t.Fatalf("PutAgentPolicy: %v", err)
	}

	resp2, err := r.Retrieve(context.Background(), readScope, req)
	if err != nil {
		t.Fatalf("Retrieve (after bind): %v", err)
	}
	after := idSet(resp2)
	if !after[authID] {
		t.Error("expected the allow-topic memory after binding")
	}
	if after[deployID] {
		t.Error("expected the non-allow-topic memory to be gone immediately after PutAgentPolicy (no stale-until-TTL window)")
	}

	// Delete via the CORE mutation path — must invalidate back to unfiltered.
	if err := r.DeleteAgentPolicy(context.Background(), scope, "bound-agent"); err != nil {
		t.Fatalf("DeleteAgentPolicy: %v", err)
	}
	resp3, err := r.Retrieve(context.Background(), readScope, req)
	if err != nil {
		t.Fatalf("Retrieve (after delete): %v", err)
	}
	afterDelete := idSet(resp3)
	if !afterDelete[authID] || !afterDelete[deployID] {
		t.Errorf("expected unfiltered results immediately after DeleteAgentPolicy, got %v", afterDelete)
	}
}

// TestRetrieve_DirectStoreMutation_DoesNotInvalidateCache proves the invalidation
// genuinely lives in the CORE (Retriever.PutAgentPolicy), not as an incidental
// side effect of the store write itself: a policy write made directly through
// st.TopicViews() (bypassing the Retriever wrapper) leaves a primed cache entry
// stale.
func TestRetrieve_DirectStoreMutation_DoesNotInvalidateCache(t *testing.T) {
	t.Parallel()
	st := openStore(t)
	gw := openMockGateway(t, 4)
	vi := vindex.New(st.Vectors(), 4, "test")
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	r := retrieval.New(st.Memories(), st.Records(), vi, gw, log).WithAgentPolicy(st.TopicViews(), true)

	scope := identity.Scope{Tenant: "t-cacheinv-direct-" + newID()}
	authID := commitMemoryFull(t, st, scope, "auth widget note qvzxf", "fact", nil, nil, nil, []string{"auth"})
	deployID := commitMemoryFull(t, st, scope, "deploy widget note qvzxf", "fact", nil, nil, nil, []string{"deploy"})

	readScope := scope
	readScope.Agent = "bound-agent"
	req := retrieval.Request{Query: "qvzxf widget", Limit: 10}

	// Prime the cache with the unbound (unfiltered) result.
	if _, err := r.Retrieve(context.Background(), readScope, req); err != nil {
		t.Fatalf("Retrieve (prime): %v", err)
	}

	// Bind DIRECTLY through the store, bypassing the Retriever's core wrapper.
	if err := st.TopicViews().PutAgentPolicy(context.Background(), scope, store.AgentPolicy{
		AgentID: "bound-agent", AllowTopics: []string{"auth"},
	}); err != nil {
		t.Fatalf("direct PutAgentPolicy: %v", err)
	}

	resp, err := r.Retrieve(context.Background(), readScope, req)
	if err != nil {
		t.Fatalf("Retrieve (after direct bind): %v", err)
	}
	got := idSet(resp)
	if !got[authID] || !got[deployID] {
		t.Errorf("a direct store mutation must NOT invalidate the cache (proves invalidation lives in the core, not incidentally elsewhere): got %v, want the STALE unfiltered set", got)
	}
}

func idSet(resp *retrieval.Response) map[string]bool {
	out := make(map[string]bool, len(resp.Items))
	for _, it := range resp.Items {
		out[it.Memory.ID] = true
	}
	return out
}
