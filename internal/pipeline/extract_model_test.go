package pipeline_test

import (
	"context"
	"testing"
	"time"

	"github.com/hurtener/stowage/internal/gateway"
	"github.com/hurtener/stowage/internal/topics"
)

// TestExtract_ModelWiring verifies the D-132 per-stage model knob reaches the
// extraction Complete call: a set model appears on the gateway request, and the
// default (unset) leaves Model empty so the gateway uses its configured model.
func TestExtract_ModelWiring(t *testing.T) {
	run := func(t *testing.T, model string) gateway.CompleteRequest {
		t.Helper()
		st := newTestStore(t)
		tenant := "t-extract-model-" + t.Name()
		recID := makeRecord(t, st, tenant, "The user prefers Go for systems programming.")
		gw := &captureGateway{json: candidateJSON(recID, "User prefers Go.")}
		svc := topics.New(st.Topics(), noopLog(), "assistant")

		stage, in := newExtractStageAndChan(st, gw, svc, "assistant")
		stage.SetModel(model)
		stage.Start(context.Background())
		in <- makeFlushedBuffer(tenant, []string{recID}, false)
		collectBatches(t, stage.Downstream(), 1, 2*time.Second)
		close(in)
		drainCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		stage.Drain(drainCtx)

		req, calls := gw.snapshot()
		if calls == 0 {
			t.Fatal("gateway Complete was not called")
		}
		return req
	}

	t.Run("model set flows to request", func(t *testing.T) {
		req := run(t, "openai/gpt-5.4-mini")
		if req.Model != "openai/gpt-5.4-mini" {
			t.Errorf("extract req.Model = %q, want %q", req.Model, "openai/gpt-5.4-mini")
		}
	})

	t.Run("default leaves model empty (gateway.model)", func(t *testing.T) {
		req := run(t, "")
		if req.Model != "" {
			t.Errorf("extract req.Model = %q, want empty", req.Model)
		}
	})
}
