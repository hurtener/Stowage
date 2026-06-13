package boot

import (
	"context"
	"fmt"
	"os"
	"sync"

	"github.com/hurtener/stowage/internal/config"
	"github.com/hurtener/stowage/internal/lifecycle"
	"github.com/hurtener/stowage/internal/pipeline"
	"github.com/hurtener/stowage/internal/reconcile"
)

// pipelineCap is the bounded ingest-channel capacity (fire-and-forget, P2).
// It mirrors internal/api's pipelineCap so every entrypoint shares one depth.
const pipelineCap = 4096

// Pipeline is the live derivation system layered on top of an opened *Stack:
// the buffer/extract/reconcile pipeline stages, the lifecycle Manager (all
// sweeps), and the embedding BackfillSweep. It is produced by StartPipeline and
// is the single canonical post-boot wiring shared by `stowage serve`,
// `stowage mcp`, and `sdk/stowage` (NewEmbedded), so the three entrypoints
// cannot drift apart again (D-068).
//
// Concurrency: Pipeline holds no per-request state; In is a channel and Stage
// is internally synchronised, so a single Pipeline is safe under concurrent
// ingest (CLAUDE.md §5). Drain is idempotent.
type Pipeline struct {
	// In is the buffer stage's input channel — the fire-and-forget ingest
	// enqueue point for the ingest handlers / SDK (P2). Send is non-blocking at
	// the call site (the ingress layers use a select/default drop).
	In chan<- pipeline.Item
	// Stage is the buffer stage, exposed for flush/branch control verbs
	// (FlushKey / FlushBranch). Wave B surfaces these on MCP and the SDK.
	Stage *pipeline.Stage

	// internal handles for Drain.
	ch        chan pipeline.Item
	buf       *pipeline.Stage
	extract   *pipeline.ExtractStage
	reconcile *reconcile.ReconcileStage
	lc        *lifecycle.Manager

	// bgCancel/bgWG govern the BackfillSweep goroutine so Drain can stop it
	// without relying on the caller cancelling ctx (no goroutine leak, AC-6).
	bgCancel context.CancelFunc
	bgWG     sync.WaitGroup

	drainOnce sync.Once
}

// StartPipeline wires and starts the live derivation system on top of an opened
// Stack: the buffer/extract/reconcile stages, the lifecycle Manager (every
// sweep), and the embedding BackfillSweep. It is the single canonical post-boot
// wiring shared by `stowage serve`, `stowage mcp`, and `sdk/stowage`
// (NewEmbedded) — boot.Open builds the static stack, StartPipeline turns it into
// a live system, and Drain tears it down.
//
// ctx governs the stage worker goroutines' logging context and the BackfillSweep
// loop; the stages themselves drain on channel close (see Drain), so they do not
// depend on ctx cancellation. The caller MUST call Drain to stop the sweeps and
// drain the stages; Drain does not close the Stack (callers own that).
//
// STOWAGE_SWEEP_FORCE, when set, runs every lifecycle sweep once synchronously
// before returning (smoke-test affordance) — identical to the prior serve-only
// behaviour, now applied uniformly on every live path.
func StartPipeline(ctx context.Context, stk *Stack, cfg config.Config) (*Pipeline, error) {
	if stk == nil {
		return nil, fmt.Errorf("boot: StartPipeline requires a non-nil Stack")
	}

	// Bounded ingest channel — fire-and-forget (P2). Owned here so the three
	// entrypoints share one depth and one lifecycle.
	ch := make(chan pipeline.Item, pipelineCap)

	// 1. Buffer stage — accumulate per-session ingest items into flush windows.
	trig := pipeline.TriggersFromConfig(cfg.Profile)
	buf := pipeline.New(stk.Store, stk.Log, trig, ch)
	buf.Start(ctx)

	// 2. Extract stage — extract memory candidates from flushed buffers.
	extract := pipeline.NewExtractStage(stk.Store, stk.Gateway, stk.TopicSvc, stk.Log, cfg.Profile, buf.Downstream())
	extract.Start(ctx)

	// 3. Reconcile stage — commit extracted candidates, embed, invalidate cache.
	rec := reconcile.New(
		stk.Store.Memories(),
		stk.Store.Ops(),
		stk.Store.Events(),
		stk.Gateway,
		stk.Log,
		extract.Downstream(),
	)
	rec.SetEmbedder(stk.Embedder)
	rec.SetScopeInvalidator(stk.Retriever.Cache()) // Phase 12 cache invalidation (D-053)
	rec.Start(ctx)

	p := &Pipeline{
		In:        ch,
		Stage:     buf,
		ch:        ch,
		buf:       buf,
		extract:   extract,
		reconcile: rec,
	}

	// 4. Embedding backfill (D-047 embed recovery) — runs on every live path, no
	// longer serve-only (closes the D-036-scenario divergence). It blocks until
	// its context is cancelled, so it gets a child context Drain can cancel.
	bgCtx, bgCancel := context.WithCancel(ctx)
	p.bgCancel = bgCancel
	p.bgWG.Add(1)
	go func() {
		defer p.bgWG.Done()
		stk.Embedder.BackfillSweep(bgCtx)
	}()

	// 5. Lifecycle sweeps — decay, dedupe, rollup, re-enqueue, confirm. The
	// re-enqueue sweep produces into ch, so it must be stopped before ch is
	// closed (handled by Drain).
	lc := lifecycle.New(stk.Store, stk.Log, lifecycle.DefaultProfile(), ch)
	if os.Getenv("STOWAGE_SWEEP_FORCE") != "" {
		stk.Log.Info("boot: STOWAGE_SWEEP_FORCE set — running all sweeps once before serving")
		lc.RunForce(ctx)
	}
	lc.Start(ctx)
	p.lc = lc

	return p, nil
}

// Drain stops the live system and is idempotent. The caller MUST have stopped
// its own ingress (HTTP accept / SDK Ingest) before calling Drain so no send
// races the channel close.
//
// Shutdown order is reverse-dependency: the upstream producers of the ingest
// channel are stopped first — the lifecycle sweeps (the re-enqueue sweep sends
// into the channel; a send on a closed channel panics, and the select/default
// does not save it) and the BackfillSweep — then the channel is closed, and only
// then are the stages drained. The stage Drain calls necessarily run in dataflow
// order (buffer → extract → reconcile): each stage's Drain closes the downstream
// channel the next stage consumes, so reconcile cannot drain before buffer.
func (p *Pipeline) Drain(ctx context.Context) error {
	p.drainOnce.Do(func() {
		// 1. Stop the producers of the ingest channel.
		if p.lc != nil {
			p.lc.Stop()
		}
		if p.bgCancel != nil {
			p.bgCancel()
		}
		p.bgWG.Wait()

		// 2. Close the ingest channel so buffer workers exit their range loop.
		close(p.ch)

		// 3. Drain the stages in dataflow order (each Drain closes the next
		//    stage's input).
		p.buf.Drain(ctx)
		p.extract.Drain(ctx)
		p.reconcile.Drain(ctx)
	})
	return nil
}
