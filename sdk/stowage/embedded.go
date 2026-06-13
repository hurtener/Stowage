package stowage

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/hurtener/stowage/internal/boot"
	"github.com/hurtener/stowage/internal/config"
	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/pipeline"
	"github.com/hurtener/stowage/internal/records"
	"github.com/hurtener/stowage/internal/retrieval"
	"github.com/hurtener/stowage/internal/store"

	// Register drivers so NewEmbedded works without manual blank-imports (sqlite
	// default — D-022; mock gateway default; hnsw vindex).
	_ "github.com/hurtener/stowage/internal/gateway/bifrost"
	_ "github.com/hurtener/stowage/internal/gateway/mock"
	_ "github.com/hurtener/stowage/internal/gateway/openaicompat"
	_ "github.com/hurtener/stowage/internal/store/pgstore"
	_ "github.com/hurtener/stowage/internal/store/sqlitestore"
	_ "github.com/hurtener/stowage/internal/vindex/hnsw"
)

// embeddedClient implements Client via in-process calls to the Stowage stack.
// It is safe for concurrent use after construction. The closer MUST NOT be
// called concurrently with any Client operation; it closes the pipeline channel,
// which would cause a panic if Ingest is called after that point.
type embeddedClient struct {
	stack *boot.Stack
	scope identity.Scope
	// pl is the live derivation system started by boot.StartPipeline. Ingest
	// enqueues onto pl.In; pl.Stage is retained for the flush/branch control
	// verbs Wave B surfaces on the SDK. Safe for concurrent use after construction.
	pl *boot.Pipeline
}

// trySend sends item to the pipeline channel in a non-blocking, panic-safe way.
// Returns false if the channel is full or closed. Uses recover to swallow the
// send-on-closed-channel panic that can occur if the closer races with a final
// Ingest call.
func trySend(ch chan<- pipeline.Item, item pipeline.Item) (sent bool) {
	defer func() {
		if r := recover(); r != nil {
			sent = false
		}
	}()
	select {
	case ch <- item:
		return true
	default:
		return false
	}
}

// NewEmbedded boots a complete in-process Stowage stack — store, gateway,
// vindex, embedder, retriever, pipeline stages, and lifecycle sweeps — and
// returns a Client backed by it.
//
// The returned closer MUST be called to drain pipeline stages and release all
// resources. Call it with a timeout context, e.g.:
//
//	ctx, cancel := context.WithCancel(context.Background())
//	client, close, err := stowage.NewEmbedded(ctx, cfg, stowage.WithTenantID("my-tenant"))
//	defer func() {
//	    cancel()            // stop background goroutines
//	    shutCtx, done := context.WithTimeout(context.Background(), 10*time.Second)
//	    defer done()
//	    close(shutCtx)
//	}()
//
// cfg must have at minimum:
//   - Store.Driver + Store.DSN (use "sqlite" with a temp path or ":memory:")
//   - Gateway.Driver (use "mock" for offline/testing; omit to accept the
//     default "mock" — embedded callers rarely need a real provider)
//
// WithTenantID is required; all SDK operations run in that tenant's scope.
func NewEmbedded(parentCtx context.Context, cfg config.Config, opts ...Option) (Client, func(context.Context) error, error) {
	o := defaultOptions()
	for _, opt := range opts {
		opt(o)
	}

	if o.tenantID == "" {
		return nil, nil, errors.New("sdk: NewEmbedded requires WithTenantID option")
	}

	// Apply sqlite + mock defaults when not explicitly set.
	if cfg.Store.Driver == "" {
		cfg.Store.Driver = "sqlite"
	}
	if cfg.Gateway.Driver == "" {
		cfg.Gateway.Driver = "mock"
	}
	if cfg.VIndex.Driver == "" {
		cfg.VIndex.Driver = "hnsw"
	}

	// Validate the minimal config subset needed for embedded mode.
	if cfg.Store.DSN == "" && cfg.Store.Driver == "sqlite" {
		return nil, nil, errors.New("sdk: NewEmbedded requires Store.DSN (use a temp file path or ':memory:')")
	}

	// innerCtx controls the lifetime of background goroutines started by boot.Open
	// (embedder processing). The caller cancels parentCtx to stop them; the closer
	// below cancels innerCtx as a belt-and-suspenders measure.
	innerCtx, cancel := context.WithCancel(parentCtx)

	cfgPtr := &cfg
	stk, err := boot.Open(innerCtx, cfgPtr)
	if err != nil {
		cancel()
		return nil, nil, fmt.Errorf("sdk: embedded boot: %w", err)
	}

	// Start the live derivation system — buffer/extract/reconcile stages,
	// lifecycle sweeps, and embedding backfill — via the single canonical
	// post-boot wiring shared with `stowage serve` and `stowage mcp` (D-068).
	// This is the same helper the server entrypoints use, so the embedded path
	// cannot drift from them.
	p, err := boot.StartPipeline(innerCtx, stk, cfg)
	if err != nil {
		cancel()
		_ = stk.Close(innerCtx)
		return nil, nil, fmt.Errorf("sdk: embedded start pipeline: %w", err)
	}

	client := &embeddedClient{
		stack: stk,
		scope: identity.Scope{Tenant: o.tenantID},
		pl:    p,
	}

	closer := func(ctx context.Context) error {
		// Drain the live system first: stop sweeps + backfill, close the ingest
		// channel, drain the stages. The closer MUST NOT be called concurrently
		// with any Client operation (Ingest sends on p.In). Then cancel the boot
		// context (stops the embedder worker) and close the stack.
		_ = p.Drain(ctx)
		cancel()
		return stk.Close(ctx)
	}

	return client, closer, nil
}

// Ingest implements Client.
func (c *embeddedClient) Ingest(ctx context.Context, req IngestRequest) (IngestResponse, error) {
	if len(req.Records) == 0 {
		return IngestResponse{}, errors.New("sdk: ingest: records must not be empty")
	}

	// Stamp + validate all items up-front.
	type stampedItem struct {
		rec       records.Record
		bufferKey string
	}
	stamped := make([]stampedItem, 0, len(req.Records))
	for i, r := range req.Records {
		rec, err := records.New(records.Input{
			TenantID:      c.scope.Tenant,
			ProjectID:     r.ProjectID,
			UserID:        r.UserID,
			SessionID:     r.SessionID,
			BranchID:      r.BranchID,
			Role:          r.Role,
			Content:       r.Content,
			SourceAgent:   r.SourceAgent,
			ResponseID:    r.ResponseID,
			Outcome:       r.Outcome,
			OutcomeDetail: r.OutcomeDetail,
			OccurredAt:    r.OccurredAt,
		})
		if err != nil {
			return IngestResponse{}, fmt.Errorf("sdk: ingest: item[%d]: %w", i, err)
		}
		stamped = append(stamped, stampedItem{rec: *rec, bufferKey: r.BufferKey})
	}

	storeRecs := make([]store.Record, len(stamped))
	for i, si := range stamped {
		storeRecs[i] = store.Record{
			ID:            si.rec.ID,
			TenantID:      si.rec.TenantID,
			ProjectID:     si.rec.ProjectID,
			UserID:        si.rec.UserID,
			SessionID:     si.rec.SessionID,
			BranchID:      si.rec.BranchID,
			Role:          si.rec.Role,
			Content:       si.rec.Content,
			SourceAgent:   si.rec.SourceAgent,
			ResponseID:    si.rec.ResponseID,
			Outcome:       si.rec.Outcome,
			OutcomeDetail: si.rec.OutcomeDetail,
			TokenEstimate: si.rec.TokenEstimate,
			OccurredAt:    si.rec.OccurredAt,
			CreatedAt:     si.rec.CreatedAt,
		}
	}

	if err := c.stack.Store.Records().Append(ctx, c.scope, storeRecs); err != nil {
		return IngestResponse{}, fmt.Errorf("sdk: ingest: append: %w", err)
	}

	allEnqueued := true
	for _, si := range stamped {
		if !trySend(c.pl.In, pipeline.Item{
			RecordID:  si.rec.ID,
			TenantID:  c.scope.Tenant,
			BufferKey: si.bufferKey,
			SessionID: si.rec.SessionID,
			BranchID:  si.rec.BranchID,
		}) {
			allEnqueued = false
		}
	}

	ids := make([]string, len(stamped))
	for i, si := range stamped {
		ids[i] = si.rec.ID
	}
	return IngestResponse{IDs: ids, Enqueued: allEnqueued}, nil
}

// Retrieve implements Client.
func (c *embeddedClient) Retrieve(ctx context.Context, req RetrieveRequest) (RetrieveResponse, error) {
	if req.Query == "" {
		return RetrieveResponse{}, errors.New("sdk: retrieve: query must not be empty")
	}

	resp, err := c.stack.Retriever.Retrieve(ctx, c.scope, retrieval.Request{
		Query:        req.Query,
		Limit:        req.Limit,
		Window:       store.Window{From: req.From, Until: req.Until},
		Kinds:        req.Kinds,
		IncludeLanes: req.IncludeLanes,
		SessionID:    req.SessionID,
		Debug:        req.Debug,
		ResponseID:   req.ResponseID,
		Profile:      req.Profile,
	})
	if err != nil {
		return RetrieveResponse{}, fmt.Errorf("sdk: retrieve: %w", err)
	}

	items := make([]MemoryItem, 0, len(resp.Items))
	for _, item := range resp.Items {
		mi := MemoryItem{
			ID:       item.Memory.ID,
			Kind:     item.Memory.Kind,
			Content:  item.Memory.Content,
			Context:  item.Memory.Context,
			Score:    item.Score,
			Citation: item.Citation,
		}
		if req.IncludeLanes {
			mi.Lanes = item.Lanes
		}
		if req.Debug && item.Breakdown != nil {
			mi.Breakdown = &RetrieveBreakdown{
				UseBoost:         item.Breakdown.UseBoost,
				NoisePenalty:     item.Breakdown.NoisePenalty,
				PrecisionFactor:  item.Breakdown.PrecisionFactor,
				ExplorationBonus: item.Breakdown.ExplorationBonus,
				DecayFactor:      item.Breakdown.DecayFactor,
				TrustMultiplier:  item.Breakdown.TrustMultiplier,
				ScopeAffinity:    item.Breakdown.ScopeAffinity,
				TemporalBoost:    item.Breakdown.TemporalBoost,
				HubDampening:     item.Breakdown.HubDampening,
				Cooldown:         item.Breakdown.Cooldown,
				ImportanceMult:   item.Breakdown.ImportanceMult,
				FinalScore:       item.Breakdown.FinalScore,
			}
		}
		items = append(items, mi)
	}

	sup := RetrieveSupport{Strength: resp.Support.Strength, TopScore: resp.Support.TopScore}
	for _, con := range resp.Support.Conflicts {
		sup.Conflicts = append(sup.Conflicts, RetrieveConflict{A: con.A, B: con.B})
	}

	return RetrieveResponse{
		ResponseID:     resp.ResponseID,
		Items:          items,
		Support:        sup,
		Degraded:       resp.Degraded,
		DegradedRerank: resp.DegradedRerank,
		CacheHit:       resp.CacheHit,
		API:            resp.API,
	}, nil
}

// Drilldown implements Client.
func (c *embeddedClient) Drilldown(ctx context.Context, req DrilldownRequest) (DrilldownResponse, error) {
	if req.MemoryID == "" && req.Citation == "" {
		return DrilldownResponse{}, errors.New("sdk: drilldown: one of memory_id or citation must be set")
	}
	if req.MemoryID != "" && req.Citation != "" {
		return DrilldownResponse{}, errors.New("sdk: drilldown: only one of memory_id or citation may be set")
	}

	memoryID := req.MemoryID
	if req.Citation != "" {
		inj, err := c.stack.Store.Injections().Get(ctx, c.scope, req.Citation)
		if err != nil {
			return DrilldownResponse{}, fmt.Errorf("sdk: drilldown: get injection: %w", err)
		}
		memoryID = inj.MemoryID
	}

	junctions, err := c.stack.Store.Memories().GetJunctions(ctx, c.scope, memoryID)
	if err != nil {
		return DrilldownResponse{}, fmt.Errorf("sdk: drilldown: get junctions: %w", err)
	}

	if len(junctions.Provenance) == 0 {
		return DrilldownResponse{MemoryID: memoryID, Spans: []DrilldownSpan{}}, nil
	}

	recordIDs := make([]string, 0, len(junctions.Provenance))
	seen := make(map[string]bool)
	for _, p := range junctions.Provenance {
		if !seen[p.RecordID] {
			recordIDs = append(recordIDs, p.RecordID)
			seen[p.RecordID] = true
		}
	}

	recs, err := c.stack.Store.Records().GetMany(ctx, c.scope, recordIDs)
	if err != nil {
		return DrilldownResponse{}, fmt.Errorf("sdk: drilldown: get records: %w", err)
	}
	recByID := make(map[string]store.Record, len(recs))
	for _, r := range recs {
		recByID[r.ID] = r
	}

	spans := make([]DrilldownSpan, 0, len(junctions.Provenance))
	for _, p := range junctions.Provenance {
		rec, ok := recByID[p.RecordID]
		if !ok {
			continue
		}
		spans = append(spans, DrilldownSpan{
			RecordID:   rec.ID,
			SpanStart:  p.SpanStart,
			SpanEnd:    p.SpanEnd,
			Excerpt:    safeExcerpt(rec.Content, p.SpanStart, p.SpanEnd),
			OccurredAt: rec.OccurredAt,
			Role:       rec.Role,
		})
	}
	return DrilldownResponse{MemoryID: memoryID, Spans: spans}, nil
}

// safeExcerpt returns content[s:e] with bounds clamped to valid range.
// Mirrors the clampExcerpt logic in internal/api.
func safeExcerpt(content string, s, e int) string {
	n := len(content)
	if s < 0 {
		s = 0
	}
	if s > n {
		s = n
	}
	if e < s {
		e = s
	}
	if e > n {
		e = n
	}
	return content[s:e]
}

// Feedback implements Client.
func (c *embeddedClient) Feedback(ctx context.Context, req FeedbackRequest) (FeedbackResponse, error) {
	if req.Signal == "" {
		return FeedbackResponse{}, errors.New("sdk: feedback: signal must be set")
	}

	switch {
	case req.Citation != "":
		if req.Signal != "wrong_citation" {
			return FeedbackResponse{}, errors.New("sdk: feedback: citation-level feedback only accepts signal wrong_citation")
		}
		if err := c.stack.Store.Injections().MarkWrongCitation(ctx, c.scope, req.Citation); err != nil {
			return FeedbackResponse{}, fmt.Errorf("sdk: feedback: wrong_citation: %w", err)
		}
		return FeedbackResponse{Applied: 1, Signal: req.Signal}, nil

	case req.MemoryID != "":
		if err := c.stack.Store.Memories().ApplyFeedback(ctx, c.scope, req.MemoryID, req.Signal); err != nil {
			return FeedbackResponse{}, fmt.Errorf("sdk: feedback: memory: %w", err)
		}
		return FeedbackResponse{Applied: 1, Signal: req.Signal}, nil

	case req.ResponseID != "":
		injections, err := c.stack.Store.Injections().ListByResponse(ctx, c.scope, req.ResponseID)
		if err != nil {
			return FeedbackResponse{}, fmt.Errorf("sdk: feedback: list injections: %w", err)
		}
		seen := make(map[string]bool)
		applied := 0
		for _, inj := range injections {
			if seen[inj.MemoryID] {
				continue
			}
			seen[inj.MemoryID] = true
			if err := c.stack.Store.Memories().ApplyFeedback(ctx, c.scope, inj.MemoryID, req.Signal); err == nil {
				applied++
			}
		}
		return FeedbackResponse{Applied: applied, Signal: req.Signal}, nil

	default:
		return FeedbackResponse{}, errors.New("sdk: feedback: one of response_id, memory_id, or citation must be set")
	}
}

// ResolveCitations implements Client.
func (c *embeddedClient) ResolveCitations(ctx context.Context, req ResolveCitationsRequest) (ResolveCitationsResponse, error) {
	if len(req.Citations) == 0 {
		return ResolveCitationsResponse{}, errors.New("sdk: resolve_citations: citations must not be empty")
	}

	type resolvedInj struct {
		citation string
		memoryID string
		rank     int
		score    float64
		lane     string
	}
	resolved := make([]resolvedInj, 0, len(req.Citations))
	notFound := make(map[string]bool)

	for _, cit := range req.Citations {
		inj, err := c.stack.Store.Injections().Get(ctx, c.scope, cit)
		if err != nil {
			notFound[cit] = true
			continue
		}
		resolved = append(resolved, resolvedInj{
			citation: cit, memoryID: inj.MemoryID,
			rank: inj.Rank, score: inj.Score, lane: inj.Lane,
		})
	}

	memIDSet := make(map[string]bool)
	memIDs := make([]string, 0, len(resolved))
	for _, ri := range resolved {
		if !memIDSet[ri.memoryID] {
			memIDSet[ri.memoryID] = true
			memIDs = append(memIDs, ri.memoryID)
		}
	}

	memByID := make(map[string]*ResolveMemory)
	provByMemID := make(map[string][]ResolveProvenanceRef)
	if len(memIDs) > 0 {
		mems, err := c.stack.Store.Memories().GetMany(ctx, c.scope, memIDs)
		if err == nil {
			for _, m := range mems {
				memByID[m.ID] = &ResolveMemory{
					ID: m.ID, Kind: m.Kind, Content: m.Content, Context: m.Context,
					Importance: m.Importance, Confidence: m.Confidence, CreatedAt: m.CreatedAt,
				}
			}
			for memID := range memByID {
				junctions, jerr := c.stack.Store.Memories().GetJunctions(ctx, c.scope, memID)
				if jerr != nil {
					provByMemID[memID] = []ResolveProvenanceRef{}
					continue
				}
				refs := make([]ResolveProvenanceRef, 0, len(junctions.Provenance))
				for _, p := range junctions.Provenance {
					refs = append(refs, ResolveProvenanceRef{
						RecordID: p.RecordID, SpanStart: p.SpanStart, SpanEnd: p.SpanEnd,
					})
				}
				provByMemID[memID] = refs
			}
		}
	}

	injByCit := make(map[string]resolvedInj, len(resolved))
	for _, ri := range resolved {
		injByCit[ri.citation] = ri
	}

	items := make([]ResolveItem, 0, len(req.Citations))
	for _, cit := range req.Citations {
		if notFound[cit] {
			items = append(items, ResolveItem{Citation: cit, Found: false})
			continue
		}
		ri, ok := injByCit[cit]
		if !ok {
			items = append(items, ResolveItem{Citation: cit, Found: false})
			continue
		}
		mem, memOK := memByID[ri.memoryID]
		if !memOK {
			items = append(items, ResolveItem{Citation: cit, Found: false})
			continue
		}
		item := ResolveItem{
			Citation: cit, Found: true, Memory: mem,
			Provenance: provByMemID[ri.memoryID],
			Rank:       ri.rank, Score: ri.score,
		}
		if ri.lane != "" {
			item.Lanes = strings.Split(ri.lane, ",")
		}
		items = append(items, item)
	}
	return ResolveCitationsResponse{Items: items}, nil
}

// Topics implements Client.
func (c *embeddedClient) Topics(ctx context.Context) (TopicsResponse, error) {
	views, err := c.stack.TopicSvc.ActiveTopics(ctx, c.scope)
	if err != nil {
		return TopicsResponse{}, fmt.Errorf("sdk: topics: %w", err)
	}
	out := make([]TopicView, 0, len(views))
	for _, v := range views {
		out = append(out, TopicView{
			Key: v.Key, Description: v.Description, Status: v.Status,
			Pack: v.Pack, Source: v.Source,
		})
	}
	return TopicsResponse{Topics: out}, nil
}

// Playbook implements Client. Stub in Phase 17.
func (c *embeddedClient) Playbook(_ context.Context, _ PlaybookRequest) (PlaybookResponse, error) {
	return PlaybookResponse{Entries: []any{}, Stub: true}, nil
}
