package stowage

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hurtener/stowage/internal/boot"
	"github.com/hurtener/stowage/internal/causal"
	"github.com/hurtener/stowage/internal/config"
	"github.com/hurtener/stowage/internal/episodes"
	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/pipeline"
	"github.com/hurtener/stowage/internal/playbook"
	"github.com/hurtener/stowage/internal/proactive"
	"github.com/hurtener/stowage/internal/reconcile"
	"github.com/hurtener/stowage/internal/records"
	"github.com/hurtener/stowage/internal/retrieval"
	"github.com/hurtener/stowage/internal/store"
	"github.com/hurtener/stowage/internal/topics"
	"github.com/hurtener/stowage/internal/traces"
	"github.com/hurtener/stowage/internal/trust"

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
	// profile is the active config profile — selects the profile-internal
	// playbook token budget (D-072/D-042).
	profile string
	// browseDefaultLimit is cfg.Retrieval.BrowseDefaultLimit (ae5, D-143) — the
	// Browse page size used when the caller omits Limit. FillZeroDefaults fills
	// it to the config default (30) before construction, so this is never a
	// silent 0 in a normally-constructed embedded client.
	browseDefaultLimit int
	// pl is the live derivation system started by boot.StartPipeline. Ingest
	// enqueues onto pl.In; pl.Stage is retained for the flush/branch control
	// verbs Wave B surfaces on the SDK. Safe for concurrent use after construction.
	pl *boot.Pipeline
}

// callScope returns the per-call read/mutate scope: the client's construction-time
// scope (c.scope — set via WithTenantID/WithProject/WithUser) with the project/user
// dimensions overridden by any non-empty per-call request values (P3, D-125). Empty
// per-call values inherit the construction default; the tenant is always the client's.
// This is the SDK analogue of the HTTP/MCP per-request project_id/user_id (D-067 parity).
func (c *embeddedClient) callScope(project, user string) identity.Scope {
	s := c.scope
	if project != "" {
		s.Project = project
	}
	if user != "" {
		s.User = user
	}
	return s
}

// trySend sends item to the pipeline channel in a non-blocking, panic-safe way.
// Returns false if the channel is full or closed. It delegates to the shared
// pipeline.TrySend so the embedded SDK and the MCP `memory_ingest` surface use
// ONE panic-safe enqueue and cannot drift apart (D-067 lens). The recover guards
// the send-on-closed-channel panic that can occur if the closer races a final
// Ingest call.
func trySend(ch chan<- pipeline.Item, item pipeline.Item) bool {
	return pipeline.TrySend(ch, item)
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
//   - Gateway.Driver — set "mock" for offline/testing. Omitting it accepts the
//     default real driver (bifrost on OpenRouter, D-131), which then REQUIRES
//     STOWAGE_GATEWAY_API_KEY at construction or NewEmbedded fails loud. An
//     embedded host that wants a keyless boot must set Gateway.Driver = "mock".
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

	// Apply the sqlite store default early so the embedded DSN-required check
	// below can key off the effective driver.
	if cfg.Store.Driver == "" {
		cfg.Store.Driver = "sqlite"
	}

	// Embedded mode requires an explicit Store.DSN for sqlite — we do NOT fall
	// back to the server's "./data/stowage.db" default, so an in-process host
	// never silently writes to an unexpected path. Checked BEFORE FillZeroDefaults
	// (which would otherwise populate the default DSN).
	if cfg.Store.DSN == "" && cfg.Store.Driver == "sqlite" {
		return nil, nil, errors.New("sdk: NewEmbedded requires Store.DSN (use a temp file path or ':memory:')")
	}

	// Apply the same defaults layer the server applies via config.Load —
	// gateway model / embedding dims / rerank model, profile, telemetry, etc. —
	// so the embedded vector + rerank lanes are populated identically to the
	// server under a documented-minimal config (D-069, parity-lens Pattern P3).
	cfg.FillZeroDefaults()

	// Run the SAME fail-loud validation the server runs before boot.Open
	// (cmd/stowage). This enforces the D-030 secret-indirection guard (a literal
	// gateway.api_key fails closed) and rejects unknown drivers/profiles, so an
	// embedded host can never stand up a stack the server would reject at boot
	// (D-069, parity-lens BUG-3). No half-built stack escapes the constructor.
	if err := cfg.Validate(); err != nil {
		return nil, nil, fmt.Errorf("sdk: NewEmbedded: invalid config: %w", err)
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
		stack:              stk,
		scope:              identity.Scope{Tenant: o.tenantID, Project: o.projectID, User: o.userID},
		profile:            cfg.Profile,
		browseDefaultLimit: cfg.Retrieval.BrowseDefaultLimit,
		pl:                 p,
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

	// Tenant is the client's auth boundary; project/user are per-call read sub-scopes (P3, D-125)
	// over the construction default (WithProject/WithUser). Empty = inherit default, then
	// tenant-wide (back-compat). The store hard-isolates to this scope.
	scope := c.callScope(req.ProjectID, req.UserID)
	resp, err := c.stack.Retriever.Retrieve(ctx, scope, retrieval.Request{
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
		if item.Stale {
			mi.Stale = true
			mi.SupersededBy = item.Memory.SupersededByID
			mi.SupersededByContent = item.SupersededByContent
			mi.SupersededByDate = item.SupersededByDate
		}
		mi.OccurredAt = item.Memory.ValidFrom
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
		Rendered:       retrieval.RenderReadBody(resp.Items),
	}, nil
}

// Drilldown implements Client.
func (c *embeddedClient) Drilldown(ctx context.Context, req DrilldownRequest) (DrilldownResponse, error) {
	scope := c.callScope(req.ProjectID, req.UserID)
	if req.MemoryID == "" && req.Citation == "" {
		return DrilldownResponse{}, errors.New("sdk: drilldown: one of memory_id or citation must be set")
	}
	if req.MemoryID != "" && req.Citation != "" {
		return DrilldownResponse{}, errors.New("sdk: drilldown: only one of memory_id or citation may be set")
	}

	memoryID := req.MemoryID
	if req.Citation != "" {
		inj, err := c.stack.Store.Injections().Get(ctx, scope, req.Citation)
		if err != nil {
			return DrilldownResponse{}, fmt.Errorf("sdk: drilldown: get injection: %w", err)
		}
		memoryID = inj.MemoryID
	}

	junctions, err := c.stack.Store.Memories().GetJunctions(ctx, scope, memoryID)
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

	recs, err := c.stack.Store.Records().GetMany(ctx, scope, recordIDs)
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
			Excerpt:    retrieval.ClampExcerpt(rec.Content, p.SpanStart, p.SpanEnd),
			OccurredAt: rec.OccurredAt,
			Role:       rec.Role,
		})
	}
	return DrilldownResponse{MemoryID: memoryID, Spans: spans}, nil
}

// Feedback implements Client.
func (c *embeddedClient) Feedback(ctx context.Context, req FeedbackRequest) (FeedbackResponse, error) {
	scope := c.callScope(req.ProjectID, req.UserID)
	if req.Signal == "" {
		return FeedbackResponse{}, errors.New("sdk: feedback: signal must be set")
	}

	switch {
	case req.Citation != "":
		if req.Signal != "wrong_citation" {
			return FeedbackResponse{}, errors.New("sdk: feedback: citation-level feedback only accepts signal wrong_citation")
		}
		if err := c.stack.Store.Injections().MarkWrongCitation(ctx, scope, req.Citation); err != nil {
			return FeedbackResponse{}, fmt.Errorf("sdk: feedback: wrong_citation: %w", err)
		}
		return FeedbackResponse{Applied: 1, Signal: req.Signal}, nil

	case req.MemoryID != "":
		if err := c.stack.Store.Memories().ApplyFeedback(ctx, scope, req.MemoryID, req.Signal); err != nil {
			return FeedbackResponse{}, fmt.Errorf("sdk: feedback: memory: %w", err)
		}
		return FeedbackResponse{Applied: 1, Signal: req.Signal}, nil

	case req.ResponseID != "":
		injections, err := c.stack.Store.Injections().ListByResponse(ctx, scope, req.ResponseID)
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
			if err := c.stack.Store.Memories().ApplyFeedback(ctx, scope, inj.MemoryID, req.Signal); err == nil {
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
	scope := c.callScope(req.ProjectID, req.UserID)
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
		inj, err := c.stack.Store.Injections().Get(ctx, scope, cit)
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
		mems, err := c.stack.Store.Memories().GetMany(ctx, scope, memIDs)
		if err == nil {
			for _, m := range mems {
				memByID[m.ID] = &ResolveMemory{
					ID: m.ID, Kind: m.Kind, Content: m.Content, Context: m.Context,
					Importance: m.Importance, Confidence: m.Confidence, CreatedAt: m.CreatedAt,
				}
			}
			for memID := range memByID {
				junctions, jerr := c.stack.Store.Memories().GetJunctions(ctx, scope, memID)
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

// Playbook implements Client via the LLM-free playbook assembly core (D-072).
// The token budget is profile-internal (config.PlaybookBudgetForProfile); the
// assembly never calls the gateway.
func (c *embeddedClient) Playbook(ctx context.Context, req PlaybookRequest) (PlaybookResponse, error) {
	scope := c.callScope(req.ProjectID, req.UserID)
	pb, err := playbook.Assemble(ctx, c.stack.Store, scope, playbook.Options{
		SessionID:   req.SessionID,
		TokenBudget: config.PlaybookBudgetForProfile(c.profile),
	})
	if err != nil {
		return PlaybookResponse{}, fmt.Errorf("sdk: playbook: %w", err)
	}
	return playbookToSDK(pb), nil
}

// Browse implements Client via the deterministic, gateway-free retrieval.Browse
// core (ae5, D-143).
func (c *embeddedClient) Browse(ctx context.Context, req BrowseRequest) (BrowseResponse, error) {
	scope := c.callScope(req.ProjectID, req.UserID)
	mode, err := retrieval.ParseBrowseMode(req.Mode)
	if err != nil {
		return BrowseResponse{}, fmt.Errorf("sdk: browse: %w", err)
	}
	res, err := retrieval.Browse(ctx, c.stack.Store, scope, retrieval.BrowseOptions{
		Mode: mode, Limit: req.Limit, Cursor: req.Cursor, DefaultLimit: c.browseDefaultLimit,
	})
	if err != nil {
		return BrowseResponse{}, fmt.Errorf("sdk: browse: %w", err)
	}
	out := BrowseResponse{Memories: make([]Memory, 0, len(res.Memories)), NextCursor: res.NextCursor}
	for _, m := range res.Memories {
		out.Memories = append(out.Memories, memoryToSDK(m))
	}
	return out, nil
}

// Episodes implements Client via the LLM-free episodic-retrieval core (D-080).
func (c *embeddedClient) Episodes(ctx context.Context, req EpisodesRequest) (EpisodesResponse, error) {
	scope := c.callScope(req.ProjectID, req.UserID)
	if req.ID != "" {
		v, err := episodes.Get(ctx, c.stack.Store, scope, req.ID)
		if errors.Is(err, store.ErrNotFound) {
			return EpisodesResponse{Episodes: []Episode{}}, nil
		}
		if err != nil {
			return EpisodesResponse{}, fmt.Errorf("sdk: episodes get: %w", err)
		}
		return EpisodesResponse{Episodes: []Episode{episodeToSDK(*v)}}, nil
	}
	// similar_to: vector-rank the scope's episodes by narrative similarity (§6b,
	// D-082). Degrades to an empty+degraded envelope when the gateway is down.
	if req.SimilarTo != "" {
		if c.stack.Retriever == nil {
			return EpisodesResponse{Episodes: []Episode{}, Degraded: true}, nil
		}
		views, degraded, err := episodes.Similar(ctx, c.stack.Store, c.stack.Retriever, scope, req.SimilarTo, req.K)
		if err != nil {
			return EpisodesResponse{}, fmt.Errorf("sdk: episodes similar: %w", err)
		}
		out := EpisodesResponse{Episodes: make([]Episode, 0, len(views)), Degraded: degraded}
		for _, v := range views {
			out.Episodes = append(out.Episodes, episodeToSDK(v))
		}
		return out, nil
	}
	// arc_of: return the episode's cross-session arc (§6b threading, D-081).
	if req.ArcOf != "" {
		views, err := episodes.Arc(ctx, c.stack.Store, scope, req.ArcOf)
		if err != nil {
			return EpisodesResponse{}, fmt.Errorf("sdk: episodes arc: %w", err)
		}
		out := EpisodesResponse{Episodes: make([]Episode, 0, len(views))}
		for _, v := range views {
			out.Episodes = append(out.Episodes, episodeToSDK(v))
		}
		return out, nil
	}
	res, err := episodes.List(ctx, c.stack.Store, scope, episodes.ListOptions{
		Limit: req.Limit, Cursor: req.Cursor, SessionID: req.SessionID, From: req.From, Until: req.Until,
	})
	if err != nil {
		return EpisodesResponse{}, fmt.Errorf("sdk: episodes list: %w", err)
	}
	out := EpisodesResponse{Episodes: make([]Episode, 0, len(res.Episodes)), NextCursor: res.NextCursor}
	for _, v := range res.Episodes {
		out.Episodes = append(out.Episodes, episodeToSDK(v))
	}
	return out, nil
}

func episodeToSDK(v episodes.EpisodeView) Episode {
	return Episode{
		ID: v.ID, SessionID: v.SessionID, Title: v.Title, Status: v.Status, Outcome: v.Outcome,
		StartedAt: v.StartedAt, EndedAt: v.EndedAt, NarrativeMemoryID: v.NarrativeMemoryID, Narrative: v.Narrative,
		Score: v.Score,
	}
}

// Causal implements Client via the gateway-free causal-traversal core (D-083).
func (c *embeddedClient) Causal(ctx context.Context, req CausalRequest) (CausalResponse, error) {
	if req.MemoryID == "" {
		return CausalResponse{}, errors.New("sdk: causal: memory_id must not be empty")
	}
	scope := c.callScope(req.ProjectID, req.UserID)
	g, err := causal.Traverse(ctx, c.stack.Store, scope, req.MemoryID, causal.Direction(req.Direction), req.Depth)
	if err != nil {
		return CausalResponse{}, fmt.Errorf("sdk: causal: %w", err)
	}
	return causalGraphToSDK(g), nil
}

// causalGraphToSDK maps the traversal graph onto the SDK wire type (byte-identical
// JSON to the HTTP/MCP envelopes — parity bar).
func causalGraphToSDK(g causal.Graph) CausalResponse {
	out := CausalResponse{Root: g.Root, Truncated: g.Truncated,
		Nodes: make([]CausalNode, 0, len(g.Nodes)), Edges: make([]CausalEdge, 0, len(g.Edges))}
	for _, n := range g.Nodes {
		cn := CausalNode{MemoryID: n.MemoryID, Kind: n.Kind, Content: n.Content, Context: n.Context, EpisodeID: n.EpisodeID}
		for _, p := range n.Provenance {
			cn.Provenance = append(cn.Provenance, CausalProvRef{RecordID: p.RecordID, SpanStart: p.SpanStart, SpanEnd: p.SpanEnd})
		}
		out.Nodes = append(out.Nodes, cn)
	}
	for _, e := range g.Edges {
		out.Edges = append(out.Edges, CausalEdge{From: e.From, To: e.To, Type: e.Type, Confidence: e.Confidence})
	}
	return out
}

// playbookToSDK maps the assembled playbook onto the SDK wire type. The JSON
// field names are byte-identical to the HTTP envelope so both impls return the
// same shape (parity_test + AC-5).
func playbookToSDK(pb *playbook.Playbook) PlaybookResponse {
	out := PlaybookResponse{
		Sections: make([]PlaybookSection, 0, len(pb.Sections)),
		Budget: PlaybookBudget{
			TokenBudget: pb.Budget.TokenBudget,
			TokensUsed:  pb.Budget.TokensUsed,
			ItemsTotal:  pb.Budget.ItemsTotal,
			ItemsPacked: pb.Budget.ItemsPacked,
		},
	}
	for _, sec := range pb.Sections {
		ss := PlaybookSection{Title: sec.Title, Kind: sec.Kind, Items: make([]PlaybookItem, 0, len(sec.Items))}
		for _, it := range sec.Items {
			si := PlaybookItem{MemoryID: it.MemoryID, Kind: it.Kind, Content: it.Content, Score: it.Score}
			for _, p := range it.Provenance {
				si.Provenance = append(si.Provenance, PlaybookProvenanceRef{
					RecordID: p.RecordID, SpanStart: p.SpanStart, SpanEnd: p.SpanEnd,
				})
			}
			ss.Items = append(ss.Items, si)
		}
		out.Sections = append(out.Sections, ss)
	}
	return out
}

// GetMemory implements Client via the reconcile core (D-070).
func (c *embeddedClient) GetMemory(ctx context.Context, id string) (GetMemoryResponse, error) {
	if id == "" {
		return GetMemoryResponse{}, errors.New("sdk: get_memory: id must not be empty")
	}
	view, err := reconcile.GetMemory(ctx, c.stack.Store, c.scope, id)
	if err != nil {
		return GetMemoryResponse{}, fmt.Errorf("sdk: get_memory: %w", err)
	}
	resp := GetMemoryResponse{
		Memory:          memoryToSDK(view.Memory),
		Entities:        view.Entities,
		Keywords:        view.Keywords,
		Queries:         view.Queries,
		SupersedesChain: view.SupersedesChain,
	}
	for _, p := range view.Provenance {
		resp.Provenance = append(resp.Provenance, MemoryProvenanceRef{
			RecordID: p.RecordID, SpanStart: p.SpanStart, SpanEnd: p.SpanEnd,
		})
	}
	return resp, nil
}

// Rollback implements Client via the reconcile core (D-064/D-070).
func (c *embeddedClient) Rollback(ctx context.Context, req RollbackRequest) (Memory, error) {
	if req.MemoryID == "" {
		return Memory{}, errors.New("sdk: rollback: memory_id must not be empty")
	}
	scope := c.callScope(req.ProjectID, req.UserID)
	res, err := reconcile.Rollback(ctx, c.stack.Store, scope, req.MemoryID, c.scopeInvalidator())
	if err != nil {
		return Memory{}, fmt.Errorf("sdk: rollback: %w", err)
	}
	// Cache invalidation now happens inside reconcile.Rollback (D-053, Wave-B
	// checkpoint) — the invalidator passed above is the single invalidation.
	if res.Memory != nil {
		return memoryToSDK(*res.Memory), nil
	}
	return Memory{ID: res.ID}, nil
}

// ResolveMemory implements Client via the reconcile core (D-065/D-070).
func (c *embeddedClient) ResolveMemory(ctx context.Context, req ResolveRequest) (ResolveResponse, error) {
	if req.MemoryID == "" {
		return ResolveResponse{}, errors.New("sdk: resolve_memory: memory_id must not be empty")
	}
	if req.Action != string(reconcile.ConfirmActionConfirm) && req.Action != string(reconcile.ConfirmActionReject) {
		return ResolveResponse{}, errors.New("sdk: resolve_memory: action must be confirm or reject")
	}
	scope := c.callScope(req.ProjectID, req.UserID)
	res, err := reconcile.Resolve(ctx, c.stack.Store, scope, req.MemoryID, reconcile.ConfirmAction(req.Action), c.scopeInvalidator())
	if err != nil {
		return ResolveResponse{}, fmt.Errorf("sdk: resolve_memory: %w", err)
	}
	// Cache invalidation (on confirm only) now happens inside reconcile.Resolve
	// (D-053, Wave-B checkpoint) — the single invalidation.
	return ResolveResponse{ID: res.ID, Status: res.Status}, nil
}

// UpsertTopics implements Client via the topics service core (D-043/D-071).
func (c *embeddedClient) UpsertTopics(ctx context.Context, req UpsertTopicsRequest) (UpsertTopicsResponse, error) {
	if len(req.Topics) == 0 {
		return UpsertTopicsResponse{}, errors.New("sdk: upsert_topics: topics must not be empty")
	}
	items := make([]topics.TopicUpsert, len(req.Topics))
	for i, t := range req.Topics {
		items[i] = topics.TopicUpsert{Key: t.Key, Description: t.Description, Status: t.Status}
	}
	n, err := c.stack.TopicSvc.Upsert(ctx, c.scope, items)
	if err != nil {
		return UpsertTopicsResponse{}, fmt.Errorf("sdk: upsert_topics: %w", err)
	}
	return UpsertTopicsResponse{Upserted: n}, nil
}

// DeleteTopic implements Client via the topics service core (D-043/D-071).
func (c *embeddedClient) DeleteTopic(ctx context.Context, key string) (DeleteTopicResponse, error) {
	if key == "" {
		return DeleteTopicResponse{}, errors.New("sdk: delete_topic: key must not be empty")
	}
	if err := c.stack.TopicSvc.Delete(ctx, c.scope, key); err != nil {
		return DeleteTopicResponse{}, fmt.Errorf("sdk: delete_topic: %w", err)
	}
	return DeleteTopicResponse{Deleted: key}, nil
}

// Flush implements Client via the retained pipeline stage (D-071).
func (c *embeddedClient) Flush(ctx context.Context, req FlushRequest) (FlushResponse, error) {
	if req.Key == "" {
		return FlushResponse{}, errors.New("sdk: flush: key must not be empty")
	}
	trigger := req.Trigger
	switch trigger {
	case "", pipeline.TriggerExplicit:
		trigger = pipeline.TriggerExplicit
	case pipeline.TriggerSessionEnd:
		// valid
	default:
		return FlushResponse{}, errors.New("sdk: flush: trigger must be explicit or session_end")
	}
	flushed := false
	if c.pl != nil && c.pl.Stage != nil {
		if err := c.pl.Stage.FlushKey(ctx, c.scope, req.Key, trigger); err != nil {
			return FlushResponse{}, fmt.Errorf("sdk: flush: %w", err)
		}
		flushed = true
	}
	return FlushResponse{Key: req.Key, Trigger: trigger, Flushed: flushed}, nil
}

// ForkBranch implements Client via the shared pipeline branch core (D-029/D-071).
func (c *embeddedClient) ForkBranch(ctx context.Context, req ForkBranchRequest) (ForkBranchResponse, error) {
	id, err := pipeline.ForkBranch(ctx, c.stack.Store, c.callScope(req.ProjectID, req.UserID), req.SessionID, req.ParentBranchID)
	if err != nil {
		return ForkBranchResponse{}, fmt.Errorf("sdk: fork_branch: %w", err)
	}
	return ForkBranchResponse{BranchID: id}, nil
}

// MergeBranch implements Client via the shared pipeline branch core (D-029/D-071).
func (c *embeddedClient) MergeBranch(ctx context.Context, branchID string) (BranchResponse, error) {
	if err := pipeline.MergeBranch(ctx, c.stack.Store, c.scope, branchID); err != nil {
		return BranchResponse{}, fmt.Errorf("sdk: merge_branch: %w", err)
	}
	return BranchResponse{BranchID: branchID, Status: "merged"}, nil
}

// DiscardBranch implements Client via the shared pipeline branch core; the
// discard flush sets SkipPromotion (D-029/D-071).
func (c *embeddedClient) DiscardBranch(ctx context.Context, branchID string) (BranchResponse, error) {
	var stage *pipeline.Stage
	if c.pl != nil {
		stage = c.pl.Stage
	}
	if err := pipeline.DiscardBranch(ctx, c.stack.Store, stage, c.scope, branchID); err != nil {
		return BranchResponse{}, fmt.Errorf("sdk: discard_branch: %w", err)
	}
	return BranchResponse{BranchID: branchID, Status: "discarded"}, nil
}

// Assert implements Client via the shared reconcile assert core (D-071).
func (c *embeddedClient) Assert(ctx context.Context, req AssertRequest) (AssertResponse, error) {
	res, err := reconcile.Assert(ctx, c.stack.Store, c.scope, reconcile.AssertParams{
		Action:   req.Action,
		MemoryID: req.MemoryID,
		Content:  req.Content,
		Kind:     req.Kind,
		Context:  req.Context,
		Review:   req.Review,
	}, c.scopeInvalidator())
	if err != nil {
		return AssertResponse{}, fmt.Errorf("sdk: assert: %w", err)
	}
	// Cache invalidation now happens inside reconcile.Assert (D-053, Wave-B
	// checkpoint) — the single invalidation.
	return AssertResponse{MemoryID: res.MemoryID, Action: res.Action, Status: res.Status}, nil
}

// Verify implements Client via the trust entailment core (D-084).
func (c *embeddedClient) Verify(ctx context.Context, req VerifyRequest) (VerifyResponse, error) {
	if req.Claim == "" {
		return VerifyResponse{}, errors.New("sdk: verify: claim must not be empty")
	}
	scope := c.callScope(req.ProjectID, req.UserID)
	v, err := trust.VerifyClaim(ctx, c.stack.Store, c.stack.Gateway, scope, req.Claim, req.Citations)
	if err != nil {
		return VerifyResponse{}, fmt.Errorf("sdk: verify: %w", err)
	}
	return VerifyResponse{Verdict: v.Verdict, Confidence: v.Confidence, Explanation: v.Explanation, Degraded: v.Degraded}, nil
}

// Review implements Client via the trust review-queue core (D-084).
func (c *embeddedClient) Review(ctx context.Context, req ReviewRequest) (ReviewResponse, error) {
	scope := c.callScope(req.ProjectID, req.UserID)
	switch req.Action {
	case "list":
		mems, next, err := trust.ListPending(ctx, c.stack.Store, scope, req.Limit, req.Cursor)
		if err != nil {
			return ReviewResponse{}, fmt.Errorf("sdk: review: %w", err)
		}
		out := ReviewResponse{Items: make([]ReviewItem, 0, len(mems)), NextCursor: next}
		for _, m := range mems {
			out.Items = append(out.Items, ReviewItem{ID: m.ID, Kind: m.Kind, Content: m.Content, Context: m.Context, CreatedAt: m.CreatedAt})
		}
		return out, nil
	case "approve", "reject":
		if req.MemoryID == "" {
			return ReviewResponse{}, errors.New("sdk: review: memory_id required for approve/reject")
		}
		res, err := trust.Resolve(ctx, c.stack.Store, scope, req.MemoryID, trust.ReviewAction(req.Action), c.scopeInvalidator())
		if err != nil {
			return ReviewResponse{}, fmt.Errorf("sdk: review: %w", err)
		}
		return ReviewResponse{ID: res.ID, Status: res.Status}, nil
	default:
		return ReviewResponse{}, errors.New("sdk: review: action must be list, approve, or reject")
	}
}

// Trace implements Client via the gateway-free trace reconstruction + signing core (D-086).
func (c *embeddedClient) Trace(ctx context.Context, req TraceRequest) (TraceResponse, error) {
	if req.ResponseID == "" {
		return TraceResponse{}, errors.New("sdk: trace: response_id must not be empty")
	}
	scope := c.callScope(req.ProjectID, req.UserID)
	tr, err := traces.Reconstruct(ctx, c.stack.Store, scope, req.ResponseID, time.Now().UnixMilli())
	if err != nil {
		return TraceResponse{}, fmt.Errorf("sdk: trace: %w", err)
	}
	b, err := traces.Sign(tr, c.stack.TraceSigner)
	if err != nil {
		return TraceResponse{}, fmt.Errorf("sdk: trace: %w", err)
	}
	return bundleToSDK(b), nil
}

// Suggestions implements Client via the proactive engine core (RFC §6d, D-087).
// list evaluates+offers; accept/dismiss resolve an offer. Governance is resolved
// from the profile default ⊕ the scope's stored override. Single-user tier — the
// admin governance read/write is HTTP/MCP only (D-067), absent here by design.
func (c *embeddedClient) Suggestions(ctx context.Context, req SuggestionsRequest) (SuggestionsResponse, error) {
	action := req.Action
	if action == "" {
		action = "list"
	}
	switch action {
	case "list":
		pc := config.ProactiveConfigForProfile(c.profile)
		def := proactive.Config{Enabled: pc.Enabled, Threshold: pc.Threshold, Budget: pc.Budget, Classes: pc.Classes}
		cfg, err := proactive.Resolve(ctx, c.stack.Store.ScopeSettings(), c.scope, def)
		if err != nil {
			return SuggestionsResponse{}, fmt.Errorf("sdk: suggestions: %w", err)
		}
		offers, degraded, err := proactive.Evaluate(ctx, c.stack.Store, c.stack.Retriever, c.scope, req.SessionID, req.Query, cfg, time.Now().UnixMilli())
		if err != nil {
			return SuggestionsResponse{}, fmt.Errorf("sdk: suggestions: %w", err)
		}
		out := SuggestionsResponse{Suggestions: make([]Suggestion, 0, len(offers)), Degraded: degraded}
		for _, o := range offers {
			out.Suggestions = append(out.Suggestions, Suggestion{
				ID: o.ID, TriggerKind: o.TriggerKind, MemoryID: o.MemoryID,
				EpisodeID: o.EpisodeID, Title: o.Title, Content: o.Content, Score: o.Score,
			})
		}
		return out, nil
	case "accept", "dismiss":
		if req.ID == "" {
			return SuggestionsResponse{}, fmt.Errorf("sdk: suggestions: id is required for %s", action)
		}
		sug, err := proactive.ResolveOffer(ctx, c.stack.Store, c.scope, req.ID, action, time.Now().UnixMilli())
		if errors.Is(err, store.ErrNotPending) || errors.Is(err, store.ErrNotFound) {
			return SuggestionsResponse{}, fmt.Errorf("sdk: suggestions: suggestion not found or already resolved")
		}
		if err != nil {
			return SuggestionsResponse{}, fmt.Errorf("sdk: suggestions: %w", err)
		}
		return SuggestionsResponse{Suggestions: []Suggestion{}, ID: sug.ID, Status: sug.Status}, nil
	default:
		return SuggestionsResponse{}, fmt.Errorf("sdk: suggestions: action must be list, accept, or dismiss")
	}
}

// bundleToSDK maps the internal trace bundle onto the SDK wire type (byte-identical
// JSON to the HTTP/MCP envelopes — the parity bar).
func bundleToSDK(b traces.Bundle) TraceResponse {
	tr := Trace{
		ResponseID: b.Trace.ResponseID, Query: b.Trace.Query, Support: b.Trace.Support,
		Degraded: b.Trace.Degraded, GeneratedAt: b.Trace.GeneratedAt,
		Items: make([]TraceItem, 0, len(b.Trace.Items)),
	}
	for _, it := range b.Trace.Items {
		item := TraceItem{
			MemoryID: it.MemoryID, Kind: it.Kind, Content: it.Content, Status: it.Status,
			Rank: it.Rank, Score: it.Score, Lane: it.Lane, WasCited: it.WasCited, Feedback: it.Feedback,
		}
		for _, p := range it.Provenance {
			item.Provenance = append(item.Provenance, TraceSpan{RecordID: p.RecordID, SpanStart: p.SpanStart, SpanEnd: p.SpanEnd, Excerpt: p.Excerpt})
		}
		for _, l := range it.Links {
			item.Links = append(item.Links, TraceLink{To: l.To, Type: l.Type, Confidence: l.Confidence})
		}
		tr.Items = append(tr.Items, item)
	}
	for _, v := range b.Trace.Verdicts {
		tr.Verdicts = append(tr.Verdicts, TraceVerdict{Claim: v.Claim, Verdict: v.Verdict, Confidence: v.Confidence, Degraded: v.Degraded})
	}
	return TraceResponse{Trace: tr, Signed: b.Signed, Algorithm: b.Algorithm, PublicKey: b.PublicKey, Signature: b.Signature}
}

// scopeInvalidator returns the retrieval-cache invalidator the reconcile core
// uses to bust stale results after a content-changing commit (D-053; D-070
// Wave-B checkpoint). It returns an untyped-nil interface when no retriever is
// wired, so the core's nil check is safe (a typed-nil *ResultCache would panic).
// Invalidation lives in the core now, so the embedded SDK no longer invalidates
// separately — exactly once, no double-invalidate.
func (c *embeddedClient) scopeInvalidator() reconcile.ScopeInvalidator {
	if c.stack != nil && c.stack.Retriever != nil {
		return c.stack.Retriever.Cache()
	}
	return nil
}

// memoryToSDK maps a store.Memory to the SDK wire type.
func memoryToSDK(m store.Memory) Memory {
	return Memory{
		ID:             m.ID,
		Kind:           m.Kind,
		Content:        m.Content,
		Context:        m.Context,
		Status:         m.Status,
		Importance:     m.Importance,
		Confidence:     m.Confidence,
		TrustSource:    m.TrustSource,
		MatchCount:     m.MatchCount,
		InjectCount:    m.InjectCount,
		UseCount:       m.UseCount,
		SaveCount:      m.SaveCount,
		FailCount:      m.FailCount,
		NoiseCount:     m.NoiseCount,
		Stability:      m.Stability,
		ValidFrom:      m.ValidFrom,
		ValidUntil:     m.ValidUntil,
		EpisodeID:      m.EpisodeID,
		SupersedesID:   m.SupersedesID,
		SupersededByID: m.SupersededByID,
		PrivacyZone:    m.PrivacyZone,
		ContentHash:    m.ContentHash,
		CreatedAt:      m.CreatedAt,
		UpdatedAt:      m.UpdatedAt,
	}
}
