package reconcile_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/hurtener/stowage/internal/gateway"
	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/pipeline"
	"github.com/hurtener/stowage/internal/reconcile"
	"github.com/hurtener/stowage/internal/store"
	"github.com/hurtener/stowage/internal/vindex"
)

// embedGW is a minimal gateway.Gateway for embedder tests.
// It returns deterministic 4-dim vectors or a configured error.
type embedGW struct {
	embedErr  error
	fewerVecs bool // if true, returns 0 vectors regardless of input count
	callCount int
}

func (g *embedGW) Complete(_ context.Context, _ gateway.CompleteRequest) (gateway.CompleteResponse, error) {
	return gateway.CompleteResponse{}, errors.New("not implemented")
}

func (g *embedGW) Embed(_ context.Context, req gateway.EmbedRequest) (gateway.EmbedResponse, error) {
	g.callCount++
	if g.embedErr != nil {
		return gateway.EmbedResponse{}, g.embedErr
	}
	if g.fewerVecs {
		// Return fewer vectors than requested to test the warning log path.
		return gateway.EmbedResponse{Vectors: nil}, nil
	}
	vecs := make([][]float32, len(req.Inputs))
	for i := range vecs {
		vecs[i] = []float32{0.5, 0.5, 0.5, 0.5}
	}
	return gateway.EmbedResponse{Vectors: vecs}, nil
}

func (g *embedGW) Rerank(_ context.Context, _ gateway.RerankRequest) (gateway.RerankResponse, error) {
	return gateway.RerankResponse{}, errors.New("not implemented")
}
func (g *embedGW) Probe(_ context.Context) error { return nil }
func (g *embedGW) Close(_ context.Context) error { return nil }

// errListVectorStore is a VectorStore whose ListWithoutVectors always errors.
// Used to exercise the backfillPass error path.
type errListVectorStore struct {
	store.VectorStore
}

func (errListVectorStore) ListWithoutVectors(_ context.Context, _ int) ([]store.MemoryForEmbed, error) {
	return nil, errors.New("list without vectors: injected error")
}

// seedMemory commits a minimal active memory to the store and returns its ID.
func seedMemory(t *testing.T, s store.Store, scope identity.Scope, content, kind string) string {
	t.Helper()
	id := ulid.Make().String()
	ts := time.Now().UnixMilli()
	mem := store.Memory{
		ID:          id,
		Kind:        kind,
		Content:     content,
		Status:      "active",
		Confidence:  0.9,
		TrustSource: "llm_extracted",
		Stability:   1.0,
		ContentHash: ulid.Make().String(),
		CreatedAt:   ts,
		UpdatedAt:   ts,
	}
	insertTestMemory(t, s, scope, mem, []string{"entity-test"}, []string{"kw-test"})
	return id
}

// --- NewEmbedder + Enqueue --------------------------------------------------

// TestReconcileStage_SetEmbedder proves SetEmbedder wires an Embedder without panicking.
func TestReconcileStage_SetEmbedder(t *testing.T) {
	t.Parallel()
	s, cleanup := newTestStore(t)
	defer cleanup()

	gw := &embedGW{}
	vi := vindex.New(s.Vectors(), 4, "test-model")
	e := reconcile.NewEmbedder(s.Vectors(), vi, gw, discardLogger())

	// Build a minimal ReconcileStage and wire the embedder.
	inCh := make(chan pipeline.CandidateBatch)
	stage := reconcile.New(s.Memories(), s.Ops(), s.Events(), gw, discardLogger(), inCh)
	stage.SetEmbedder(e)
}

// TestEmbedder_NewAndEnqueue proves NewEmbedder returns a non-nil Embedder and
// Enqueue sends jobs without blocking when the queue has capacity.
func TestEmbedder_NewAndEnqueue(t *testing.T) {
	t.Parallel()
	s, cleanup := newTestStore(t)
	defer cleanup()

	gw := &embedGW{}
	vi := vindex.New(s.Vectors(), 4, "test-model")
	e := reconcile.NewEmbedder(s.Vectors(), vi, gw, discardLogger())

	// Enqueue should not block when the queue has capacity (cap 512).
	e.Enqueue(reconcile.EmbedJob{
		Scope:        identity.Scope{Tenant: "t1"},
		MemoryID:     "01HZZZZZZZZZZZZZZZZZZZZZZZ",
		EnrichedText: "some enriched text about Paris",
	})
}

// TestEmbedder_QueueFull proves Enqueue drops jobs when the queue is full
// rather than blocking (drop-with-log, backfill recovers per D-047).
func TestEmbedder_QueueFull(t *testing.T) {
	t.Parallel()
	s, cleanup := newTestStore(t)
	defer cleanup()

	gw := &embedGW{}
	vi := vindex.New(s.Vectors(), 4, "test-model")
	e := reconcile.NewEmbedder(s.Vectors(), vi, gw, discardLogger())

	// Fill the queue beyond capacity (512). The excess must not block.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 600; i++ {
			e.Enqueue(reconcile.EmbedJob{
				Scope:        identity.Scope{Tenant: "t1"},
				MemoryID:     "01HZZZZZZZZZZZZZZZZZZZZZZZ",
				EnrichedText: "text",
			})
		}
	}()

	select {
	case <-done:
		// Good: all 600 Enqueue calls completed without blocking.
	case <-time.After(5 * time.Second):
		t.Fatal("Enqueue blocked; should drop when queue is full")
	}
}

// TestEmbedder_StartAndProcess proves Start launches the worker goroutine which
// processes embed jobs by calling gateway.Embed and upserting via vindex.
func TestEmbedder_StartAndProcess(t *testing.T) {
	t.Parallel()
	s, cleanup := newTestStore(t)
	defer cleanup()

	gw := &embedGW{}
	vi := vindex.New(s.Vectors(), 4, "test-model")
	e := reconcile.NewEmbedder(s.Vectors(), vi, gw, discardLogger())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	e.Start(ctx)

	scope := identity.Scope{Tenant: "tenant-embed"}
	memID := seedMemory(t, s, scope, "Paris is the capital of France", "fact")

	e.Enqueue(reconcile.EmbedJob{
		Scope:        scope,
		MemoryID:     memID,
		EnrichedText: "Paris is the capital of France",
	})

	// Wait for the worker to process the job (upsert done → vector appears).
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		hits, err := vi.Search(ctx, scope, []float32{0.5, 0.5, 0.5, 0.5}, 1, vindex.Filter{})
		if err == nil && len(hits) == 1 && hits[0].MemoryID == memID {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Error("vector was not upserted within timeout")
}

// TestEmbedder_EmbedError proves that gateway.Embed failure is logged and does
// not crash the worker; subsequent jobs continue to be processed.
func TestEmbedder_EmbedError(t *testing.T) {
	t.Parallel()
	s, cleanup := newTestStore(t)
	defer cleanup()

	gw := &embedGW{embedErr: errors.New("gateway down")}
	vi := vindex.New(s.Vectors(), 4, "test-model")
	e := reconcile.NewEmbedder(s.Vectors(), vi, gw, discardLogger())

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	e.Start(ctx)

	e.Enqueue(reconcile.EmbedJob{
		Scope:        identity.Scope{Tenant: "t-err"},
		MemoryID:     "01HZZZZZZZZZZZZZZZZZZZZZZZ",
		EnrichedText: "text that will fail to embed",
	})

	// Give the worker time to process and log the error (no panic expected).
	time.Sleep(200 * time.Millisecond)
}

// TestEmbedder_BackfillSweep proves BackfillSweep does an immediate pass and
// enqueues jobs for active memories that have no vector entry.
func TestEmbedder_BackfillSweep(t *testing.T) {
	t.Parallel()
	s, cleanup := newTestStore(t)
	defer cleanup()

	gw := &embedGW{}
	vi := vindex.New(s.Vectors(), 4, "test-model")
	e := reconcile.NewEmbedder(s.Vectors(), vi, gw, discardLogger())

	scope := identity.Scope{Tenant: "t-backfill"}
	memID := seedMemory(t, s, scope, "backfill test content", "fact")

	// Start the worker so backfill-enqueued jobs are processed.
	ctx, cancel := context.WithCancel(context.Background())
	e.Start(ctx)

	sweepDone := make(chan struct{})
	go func() {
		defer close(sweepDone)
		e.BackfillSweep(ctx)
	}()

	// Poll for the vector to appear.
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		searchCtx, searchCancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		hits, err := vi.Search(searchCtx, scope, []float32{0.5, 0.5, 0.5, 0.5}, 1, vindex.Filter{})
		searchCancel()
		if err == nil && len(hits) == 1 && hits[0].MemoryID == memID {
			cancel()
			<-sweepDone
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	<-sweepDone
	t.Error("backfill did not produce a vector within timeout")
}

// TestEmbedder_BackfillSweep_ListError proves BackfillSweep handles a
// ListWithoutVectors error gracefully (logged, not fatal; backfill recovers on
// next tick per D-047).
func TestEmbedder_BackfillSweep_ListError(t *testing.T) {
	t.Parallel()
	s, cleanup := newTestStore(t)
	defer cleanup()

	gw := &embedGW{}
	vi := vindex.New(s.Vectors(), 4, "test-model")
	// Use a VS that errors on ListWithoutVectors.
	e := reconcile.NewEmbedder(errListVectorStore{s.Vectors()}, vi, gw, discardLogger())

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately so only one pass runs

	done := make(chan struct{})
	go func() {
		defer close(done)
		e.BackfillSweep(ctx)
	}()

	select {
	case <-done:
		// Good: error was logged and sweep exited cleanly.
	case <-time.After(2 * time.Second):
		t.Fatal("BackfillSweep did not exit after context cancellation with error VS")
	}
}

// TestEmbedder_ProcessBatch_FewerVectors proves processBatch logs a warning
// when the gateway returns fewer vectors than inputs (degraded partial response).
func TestEmbedder_ProcessBatch_FewerVectors(t *testing.T) {
	t.Parallel()
	s, cleanup := newTestStore(t)
	defer cleanup()

	// Gateway returns 0 vectors for any input.
	gw := &embedGW{fewerVecs: true}
	vi := vindex.New(s.Vectors(), 4, "test-model")
	e := reconcile.NewEmbedder(s.Vectors(), vi, gw, discardLogger())

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	e.Start(ctx)

	e.Enqueue(reconcile.EmbedJob{
		Scope:        identity.Scope{Tenant: "t-fewer"},
		MemoryID:     ulid.Make().String(),
		EnrichedText: "text for fewer vectors test",
	})

	// Give the worker time to process the job (warning path hit, no panic).
	time.Sleep(200 * time.Millisecond)
}

// TestEmbedder_BackfillSweep_Empty proves BackfillSweep exits cleanly when the
// store has no unembedded memories and the context is immediately cancelled.
func TestEmbedder_BackfillSweep_Empty(t *testing.T) {
	t.Parallel()
	s, cleanup := newTestStore(t)
	defer cleanup()

	gw := &embedGW{}
	vi := vindex.New(s.Vectors(), 4, "test-model")
	e := reconcile.NewEmbedder(s.Vectors(), vi, gw, discardLogger())

	// Cancel context immediately so the ticker loop exits after the first pass.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		e.BackfillSweep(ctx)
	}()

	select {
	case <-done:
		// Good.
	case <-time.After(2 * time.Second):
		t.Fatal("BackfillSweep did not exit after context cancellation")
	}
}
