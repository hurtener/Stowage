package reconcile

import (
	"context"
	"log/slog"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/hurtener/stowage/internal/gateway"
	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/store"
	"github.com/hurtener/stowage/internal/vindex"
)

const (
	// embedQueueCap is the bounded channel capacity for embed jobs (D-047).
	// Drop-with-log when full; backfill sweep recovers.
	embedQueueCap = 512

	// embedBatchSize is the max inputs per gateway.Embed call.
	embedBatchSize = 64

	// backfillBatchSize is the ListWithoutVectors batch size for the backfill sweep.
	backfillBatchSize = 64

	// backfillTickMin is the lower bound of the jittered backfill ticker interval.
	backfillTickMin = 5 * time.Minute

	// backfillTickJitter is the additional jitter applied to the backfill ticker.
	backfillTickJitter = 2 * time.Minute
)

// EmbedJob is an embed request for a single memory (D-047).
type EmbedJob struct {
	Scope        identity.Scope
	MemoryID     string
	EnrichedText string // content + entities + keywords + anticipated_queries
}

// Embedder asynchronously embeds memories and persists vectors (D-047).
// It is safe for concurrent use after New returns; Start must be called once.
type Embedder struct {
	vs  store.VectorStore
	vi  vindex.Index
	gw  gateway.Gateway
	log *slog.Logger
	ch  chan EmbedJob
}

// NewEmbedder creates an Embedder wired to the given dependencies.
// Call Start to begin processing.
func NewEmbedder(vs store.VectorStore, vi vindex.Index, gw gateway.Gateway, log *slog.Logger) *Embedder {
	return &Embedder{
		vs:  vs,
		vi:  vi,
		gw:  gw,
		log: log.With("subsystem", "embedder"),
		ch:  make(chan EmbedJob, embedQueueCap),
	}
}

// Enqueue non-blockingly enqueues an embed job. If the channel is full the job
// is dropped and a warning is logged; the backfill sweep will recover it (D-047).
func (e *Embedder) Enqueue(job EmbedJob) {
	select {
	case e.ch <- job:
	default:
		e.log.Warn("embedder: queue full — embed job dropped; backfill will recover",
			"memory_id", job.MemoryID, "tenant", job.Scope.Tenant)
	}
}

// Start launches the embed worker goroutine. It reads from the internal job
// channel in batches of up to embedBatchSize, calls gateway.Embed, and upserts
// the resulting vectors via vindex.Index. Failures are logged at Warn. The
// goroutine exits when ctx is cancelled.
func (e *Embedder) Start(ctx context.Context) {
	go e.worker(ctx)
}

// BackfillSweep runs an immediate pass then a jittered periodic sweep that
// finds active memories without vectors (store.ListWithoutVectors), builds
// enriched text, and enqueues embed jobs. Blocks until ctx is cancelled.
// Call in a separate goroutine (main.go wires this).
func (e *Embedder) BackfillSweep(ctx context.Context) {
	// Immediate pass at startup.
	e.backfillPass(ctx)

	// Jittered periodic ticker (D-047 backfill recovery).
	for {
		jitter := time.Duration(rand.Int64N(int64(backfillTickJitter))) //nolint:gosec // non-crypto jitter for ticker interval
		ticker := time.NewTimer(backfillTickMin + jitter)
		select {
		case <-ctx.Done():
			ticker.Stop()
			return
		case <-ticker.C:
			e.backfillPass(ctx)
		}
	}
}

// worker drains the job channel in batches of up to embedBatchSize.
func (e *Embedder) worker(ctx context.Context) {
	for {
		// Block until at least one job or context cancellation.
		var jobs []EmbedJob
		select {
		case <-ctx.Done():
			return
		case job, ok := <-e.ch:
			if !ok {
				return
			}
			jobs = append(jobs, job)
		}

		// Drain up to embedBatchSize-1 additional jobs without blocking.
		draining := true
		for draining && len(jobs) < embedBatchSize {
			select {
			case job, ok := <-e.ch:
				if !ok {
					draining = false
				} else {
					jobs = append(jobs, job)
				}
			default:
				draining = false
			}
		}

		e.processBatch(ctx, jobs)
	}
}

// processBatch embeds a batch of jobs and upserts the resulting vectors.
func (e *Embedder) processBatch(ctx context.Context, jobs []EmbedJob) {
	if len(jobs) == 0 {
		return
	}

	inputs := make([]string, len(jobs))
	for i, j := range jobs {
		inputs[i] = j.EnrichedText
	}

	resp, err := e.gw.Embed(ctx, gateway.EmbedRequest{Inputs: inputs})
	if err != nil {
		e.log.WarnContext(ctx, "embedder: gateway.Embed failed — jobs dead-lettered", "err", err, "count", len(jobs))
		// Per D-047: failures dead-letter (stage "embed"); retrieval still serves
		// lexically (degraded per-memory, not per-system). We log and move on;
		// the backfill sweep will retry these memories on the next pass.
		return
	}

	for i, job := range jobs {
		if i >= len(resp.Vectors) {
			e.log.WarnContext(ctx, "embedder: gateway returned fewer vectors than inputs",
				"got", len(resp.Vectors), "want", len(jobs))
			break
		}
		vec := resp.Vectors[i]
		scope := job.Scope
		if err := e.vi.Upsert(ctx, scope, job.MemoryID, vec); err != nil {
			e.log.WarnContext(ctx, "embedder: vindex.Upsert failed",
				"memory_id", job.MemoryID, "tenant", scope.Tenant, "err", err)
		}
	}
}

// backfillPass scans for active memories without vectors and enqueues embed jobs.
func (e *Embedder) backfillPass(ctx context.Context) {
	mems, err := e.vs.ListWithoutVectors(ctx, backfillBatchSize)
	if err != nil {
		e.log.WarnContext(ctx, "embedder: backfill ListWithoutVectors failed", "err", err)
		return
	}
	if len(mems) == 0 {
		return
	}
	e.log.InfoContext(ctx, "embedder: backfill sweep enqueuing", "count", len(mems))
	for _, m := range mems {
		e.Enqueue(EmbedJob{
			Scope:        identity.Scope{Tenant: m.TenantID},
			MemoryID:     m.MemoryID,
			EnrichedText: buildEnrichedText(m),
		})
	}
}

// buildEnrichedText concatenates content, entities, keywords, and anticipated
// queries into one string for embedding (D-047 enriched-text embedding).
func buildEnrichedText(m store.MemoryForEmbed) string {
	parts := make([]string, 0, 1+len(m.Entities)+len(m.Keywords)+len(m.Queries))
	if m.Content != "" {
		parts = append(parts, m.Content)
	}
	parts = append(parts, m.Entities...)
	parts = append(parts, m.Keywords...)
	parts = append(parts, m.Queries...)
	return strings.Join(parts, " ")
}
