package reconcile_test

import (
	"context"
	"encoding/json"
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

// TestStageVectorNeighborDrivesSupersede proves A4 done right: a candidate that shares
// NO entity/keyword token with an existing memory (so structural FindNeighbors misses
// it) but is semantically related (cosine ≥ floor) reaches the LLM reconcile DECISION
// via the vector lane — which can then SUPERSEDE (the contradiction-handling path the
// cosine-only auto-discard would have silently swallowed). Without the augmentation this
// would be a fast-add (no LLM call), leaving the stale memory un-superseded.
func TestStageVectorNeighborDrivesSupersede(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-vec-supersede")

	vec := []float32{1, 0, 0, 0}
	vi := vindex.New(st.Vectors(), 4, "test-model")

	// Existing (stale) memory M with its vector. Different content + tokens from the candidate.
	target := store.Memory{
		ID: ulid.Make().String(), Kind: "fact", Content: "The deploy script runs on push",
		Status: "active", Confidence: 0.8, TrustSource: "llm_extracted", Stability: 1.0,
		ContentHash: reconcile.ContentHash(reconcile.NormalizeContent("The deploy script runs on push")),
		CreatedAt:   time.Now().UnixMilli(), UpdatedAt: time.Now().UnixMilli(),
	}
	insertTestMemory(t, st, scope, target, []string{"ent-deploy"}, []string{"kw-push"})
	if err := vi.Upsert(ctx, scope, target.ID, vec); err != nil {
		t.Fatalf("vindex upsert: %v", err)
	}

	// Candidate: a CORRECTION phrased with no shared token → structural miss; the const
	// gateway embeds it to the same vector → vector neighbor finds M. The LLM (stub)
	// returns supersede(M) — the contradiction path that must stay reachable.
	cand := newCandidate("fact", "Deployment is now triggered manually only", 4, 0.9, "ent-manual")
	cand.Keywords = []string{"kw-manual"}

	gw := &constVecGateway{
		stubGateway: stubGateway{responses: []gateway.CompleteResponse{
			{JSON: json.RawMessage(`{"action":"supersede","target_ids":["` + target.ID + `"],"reason":"correction"}`)},
		}},
		vec: vec,
	}
	ch := make(chan pipeline.CandidateBatch, 1)
	ch <- pipeline.CandidateBatch{Scope: scope, Candidates: []pipeline.Candidate{cand}}
	close(ch)
	stage := reconcile.New(st.Memories(), st.Ops(), st.Events(), gw, discardLogger(), ch)
	stage.SetVIndex(vi)
	sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	stage.Start(sctx)
	stage.Drain(sctx)

	// The LLM reconcile WAS consulted (the semantic neighbor prevented a silent fast-add).
	if gw.calls != 1 {
		t.Errorf("gateway Complete called %d times, want 1 (semantic neighbor should reach the LLM)", gw.calls)
	}
	// The stale memory was SUPERSEDED — the contradiction was handled, not swallowed.
	updated, err := st.Memories().Get(ctx, scope, target.ID)
	if err != nil {
		t.Fatalf("get target: %v", err)
	}
	if updated.Status != "superseded" {
		t.Errorf("stale memory status = %q, want superseded (semantic neighbor must drive the supersede path)", updated.Status)
	}
}
