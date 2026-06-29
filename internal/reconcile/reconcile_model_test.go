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
)

// TestStageModelWiring verifies the D-132 per-stage model knob reaches the reconcile
// decision Complete call. With a neighbor present (so the LLM decision path runs), a
// set model appears on the gateway request; the default (unset) leaves Model empty.
func TestStageModelWiring(t *testing.T) {
	const entity = "model-entity"
	const kw = "model-kw"

	run := func(t *testing.T, model string) gateway.CompleteRequest {
		t.Helper()
		st, cleanup := newTestStore(t)
		defer cleanup()
		scope := tenantScope("t-model-" + t.Name())

		target := store.Memory{
			ID:          ulid.Make().String(),
			Kind:        "fact",
			Content:     "Python uses dynamic typing",
			Status:      "active",
			Importance:  3,
			Confidence:  0.9,
			TrustSource: "llm_extracted",
			Stability:   1.0,
			ContentHash: reconcile.ContentHash(reconcile.NormalizeContent("Python uses dynamic typing")),
			ValidFrom:   time.Now().UnixMilli(),
			CreatedAt:   time.Now().UnixMilli(),
			UpdatedAt:   time.Now().UnixMilli(),
		}
		insertTestMemory(t, st, scope, target, []string{entity}, []string{kw})

		gw := &stubGateway{responses: []gateway.CompleteResponse{
			{JSON: json.RawMessage(`{"action":"add","reason":"distinct fact"}`)},
		}}

		ch := make(chan pipeline.CandidateBatch, 1)
		cand := newCandidate("fact", "Rust uses static typing", 3, 0.9, entity)
		cand.Keywords = []string{kw}
		ch <- pipeline.CandidateBatch{Scope: scope, Candidates: []pipeline.Candidate{cand}}
		close(ch)

		stage := reconcile.New(st.Memories(), st.Ops(), st.Events(), gw, discardLogger(), ch)
		stage.SetModel(model)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		stage.Start(ctx)
		stage.Drain(ctx)

		if gw.calls == 0 {
			t.Fatal("gateway Complete was not called — decision path did not run")
		}
		return gw.lastReq
	}

	t.Run("model set flows to request", func(t *testing.T) {
		req := run(t, "anthropic/claude-haiku-4.5")
		if req.Model != "anthropic/claude-haiku-4.5" {
			t.Errorf("decision req.Model = %q, want %q", req.Model, "anthropic/claude-haiku-4.5")
		}
	})

	t.Run("default leaves model empty (gateway.model)", func(t *testing.T) {
		req := run(t, "")
		if req.Model != "" {
			t.Errorf("decision req.Model = %q, want empty", req.Model)
		}
	})
}
