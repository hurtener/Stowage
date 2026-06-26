package pipeline_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/hurtener/stowage/internal/gateway"
	"github.com/hurtener/stowage/internal/topics"
)

// captureGateway records the last Complete request so a test can assert the fields
// the pipeline set on it (e.g. ReasoningEffort, D-128).
type captureGateway struct {
	mu      sync.Mutex
	lastReq gateway.CompleteRequest
	calls   int
	json    []byte
}

func (g *captureGateway) Complete(_ context.Context, req gateway.CompleteRequest) (gateway.CompleteResponse, error) {
	g.mu.Lock()
	g.lastReq = req
	g.calls++
	g.mu.Unlock()
	return gateway.CompleteResponse{JSON: g.json}, nil
}

func (g *captureGateway) Embed(_ context.Context, _ gateway.EmbedRequest) (gateway.EmbedResponse, error) {
	return gateway.EmbedResponse{}, nil
}

func (g *captureGateway) Rerank(_ context.Context, _ gateway.RerankRequest) (gateway.RerankResponse, error) {
	return gateway.RerankResponse{}, nil
}
func (g *captureGateway) Probe(_ context.Context) error { return nil }
func (g *captureGateway) Close(_ context.Context) error { return nil }

func (g *captureGateway) snapshot() (gateway.CompleteRequest, int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.lastReq, g.calls
}

// TestExtract_ReasoningEffortWiring verifies the D-128 learner reasoning-effort knob
// reaches the extraction Complete call: the configured effort appears on the gateway
// request, and the default (unset) sends no reasoning param.
func TestExtract_ReasoningEffortWiring(t *testing.T) {
	run := func(t *testing.T, effort string) gateway.CompleteRequest {
		t.Helper()
		st := newTestStore(t)
		tenant := "t-extract-effort-" + t.Name()
		recID := makeRecord(t, st, tenant, "The user prefers Go for systems programming.")
		gw := &captureGateway{json: candidateJSON(recID, "User prefers Go.")}
		svc := topics.New(st.Topics(), noopLog(), "assistant")

		stage, in := newExtractStageAndChan(st, gw, svc, "assistant")
		stage.SetReasoningEffort(effort)
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

	t.Run("effort set flows to request", func(t *testing.T) {
		req := run(t, "low")
		if req.ReasoningEffort != "low" {
			t.Errorf("extract req.ReasoningEffort = %q, want %q", req.ReasoningEffort, "low")
		}
	})

	t.Run("default sends no reasoning param", func(t *testing.T) {
		req := run(t, "")
		if req.ReasoningEffort != "" {
			t.Errorf("extract req.ReasoningEffort = %q, want empty", req.ReasoningEffort)
		}
	})
}
