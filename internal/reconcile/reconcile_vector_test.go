package reconcile_test

import (
	"context"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/hurtener/stowage/internal/gateway"
	"github.com/hurtener/stowage/internal/pipeline"
	"github.com/hurtener/stowage/internal/reconcile"
	"github.com/hurtener/stowage/internal/store"
	"github.com/hurtener/stowage/internal/vindex"
)

// constVecGateway embeds every input to the same fixed vector, so two LEXICALLY
// different texts get cosine 1.0 — lets us exercise the semantic-neighbor path
// deterministically (the mock gateway embeds per-content, which can't do this).
type constVecGateway struct {
	stubGateway
	vec []float32
}

func (g *constVecGateway) Embed(_ context.Context, _ gateway.EmbedRequest) (gateway.EmbedResponse, error) {
	return gateway.EmbedResponse{Vectors: [][]float32{g.vec}}, nil
}

// TestStageVectorNeighborNearDup proves A4+A5: a candidate that shares NO entity/
// keyword token with an existing memory (so structural FindNeighbors misses it) but is
// semantically identical (cosine ≥ threshold) is caught as a near-dup via the vector
// lane — match_count bumped, no duplicate memory created, no LLM call.
func TestStageVectorNeighborNearDup(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-vec-neardup")

	vec := []float32{1, 0, 0, 0}
	vi := vindex.New(st.Vectors(), 4, "test-model")

	// Existing memory M with its vector. Different content + entity from the candidate.
	target := store.Memory{
		ID: ulid.Make().String(), Kind: "fact", Content: "Guido created Python",
		Status: "active", Confidence: 0.8, TrustSource: "llm_extracted", Stability: 1.0,
		ContentHash: reconcile.ContentHash(reconcile.NormalizeContent("Guido created Python")),
		CreatedAt:   time.Now().UnixMilli(), UpdatedAt: time.Now().UnixMilli(),
	}
	insertTestMemory(t, st, scope, target, []string{"ent-guido"}, []string{"kw-python"})
	if err := vi.Upsert(ctx, scope, target.ID, vec); err != nil {
		t.Fatalf("vindex upsert: %v", err)
	}

	// Candidate: lexically unrelated, no shared entity/keyword → structural miss; the
	// const gateway embeds it to the SAME vector → vector neighbor finds M at cosine 1.
	cand := newCandidate("fact", "The Python language was authored by its BDFL", 4, 0.9, "ent-bdfl")
	cand.Keywords = []string{"kw-bdfl"}

	gw := &constVecGateway{vec: vec}
	ch := make(chan pipeline.CandidateBatch, 1)
	ch <- pipeline.CandidateBatch{Scope: scope, Candidates: []pipeline.Candidate{cand}}
	close(ch)
	stage := reconcile.New(st.Memories(), st.Ops(), st.Events(), gw, discardLogger(), ch)
	stage.SetVIndex(vi)
	sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	stage.Start(sctx)
	stage.Drain(sctx)

	// No new memory (the semantic near-dup was discarded).
	mems, _, _ := st.Memories().ListByStatus(ctx, scope, "active", 10, "")
	if len(mems) != 1 {
		t.Fatalf("semantic near-dup: got %d active memories, want 1 (vector neighbor not consulted?)", len(mems))
	}
	// The LLM reconcile path must NOT have been called (near-dup fires first).
	if gw.calls != 0 {
		t.Errorf("semantic near-dup: gateway Complete called %d times, want 0", gw.calls)
	}
	// match_count bumped on the existing memory.
	updated, err := st.Memories().Get(ctx, scope, target.ID)
	if err != nil {
		t.Fatalf("get target: %v", err)
	}
	if updated.MatchCount != 1 {
		t.Errorf("match_count = %d, want 1 (semantic near-dup should bump it)", updated.MatchCount)
	}
}
