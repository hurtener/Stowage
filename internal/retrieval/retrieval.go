// Package retrieval implements the four-lane retrieval read path (Phase 09/11,
// RFC §4.2). Lanes run concurrently via errgroup; results are fused by RRF
// then re-ranked by the utility scoring function (Phase 10, RFC §5.2).
//
// Lanes:
//   - lexical:    FTS5/tsvector over content+context (always)
//   - queries:    FTS over anticipated_queries (always)
//   - structured: entity/keyword overlap via FindNeighbors (always)
//   - vector:     gateway embed + vindex cosine (skipped when gateway is down)
//
// When the vector lane is unavailable (gateway.ErrGatewayUnavailable or any
// other gateway error) the response carries degraded:true; the other three
// lanes are intact (D-036).
//
// match_count is incremented asynchronously for every returned memory;
// failures are logged but never returned to the caller.
//
// ActivityTurns (Phase 10, deepened): for the scoring decay function each memory
// gets its TRUE activity-turn count — the number of records in scope created after
// THAT memory's last_accessed_at. We fetch the scope's record timestamps newer than
// the oldest result's last_accessed once (capped at activityTurnsScanCap) and count
// per item in memory via binary search, so there are no per-item round-trips.
//
// Phase 11: every retrieve call is recorded as injection rows (async, zero added
// latency via InjectionWriter). The envelope graduates to api:"v1" with
// per-item citation handles (= injection ULIDs, D-051) and a top-level
// response_id. Profile presets control lane/scoring parameters.
package retrieval

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sort"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/oklog/ulid/v2"

	"github.com/hurtener/stowage/internal/gateway"
	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/scoring"
	"github.com/hurtener/stowage/internal/store"
	"github.com/hurtener/stowage/internal/vindex"
)

const (
	// maxLimit is the hard cap on the number of items returned.
	maxLimit = 50
)

// Request is the retrieve request payload.
type Request struct {
	Query        string       // free-text query (required)
	Limit        int          // max results; capped to maxLimit
	Window       store.Window // optional time window on created_at
	Kinds        []string     // optional kind filter; empty = all
	IncludeLanes bool         // include per-item lane provenance in response
	Debug        bool         // include per-item Breakdown in response (Phase 10)

	// SessionID identifies the caller's current session. Used for:
	//   1. Write-echo cooldown: memories extracted in this session are
	//      suppressed for 30 min (Phase 10, see scoring package).
	//   2. Future: project-affinity scoring.
	SessionID string

	// ResponseID is a caller-supplied identifier for this retrieval response.
	// When absent a ULID is generated and returned in the envelope (D-051).
	ResponseID string

	// Profile selects a named retrieval preset: "precise", "balanced" (default),
	// or "broad". Invalid values are rejected by the handler (D-034).
	Profile string
}

// MemoryItem is one retrieval result.
type MemoryItem struct {
	Memory    store.Memory
	Score     float64            // utility-adjusted score (Phase 10; RRF score before Phase 10)
	Lanes     []string           // populated when Request.IncludeLanes is true
	Breakdown *scoring.Breakdown // populated when Request.Debug is true
	Citation  string             // injection ULID = citation handle (Phase 11, D-051)
}

// Response is the retrieve response payload.
type Response struct {
	ResponseID     string // caller-supplied or generated ULID (D-051)
	Items          []MemoryItem
	Support        Support // per-response evidence summary (Phase 10, RFC §4.2.5)
	Degraded       bool    // true when the vector lane was skipped (D-036)
	API            string  // "v1" (Phase 11)
	CacheHit       bool    // true when the result was served from the result cache (Phase 12)
	DegradedRerank bool    // true when the rerank pass failed and Phase-10 order was preserved (Phase 12)
}

// Retriever executes the four-lane retrieval and returns fused + scored results.
// It is safe for concurrent use after New returns.
// Call Close to drain the injection writer goroutine on shutdown.
type Retriever struct {
	mem         store.MemoryStore
	recs        store.RecordStore
	vi          vindex.Index
	gw          gateway.Gateway
	hub         *Hub
	log         *slog.Logger
	injWr       *InjectionWriter // nil when no injection store is wired
	cache       *ResultCache
	hotSet      *HotSet
	rerankModel string           // cross-encoder model; empty = use gateway default
	grantsSt    store.GrantStore // nil when grants are not wired (Phase 15, D-060)
}

// New creates a Retriever wired to the given dependencies.
// recs is used to compute ActivityTurns for the scoring decay function.
// injSt may be nil (injection recording disabled — no injection writer started).
func New(mem store.MemoryStore, recs store.RecordStore, vi vindex.Index, gw gateway.Gateway, log *slog.Logger) *Retriever {
	return &Retriever{
		mem:    mem,
		recs:   recs,
		vi:     vi,
		gw:     gw,
		hub:    NewHub(hubMaxSize),
		log:    log.With("subsystem", "retrieval"),
		cache:  NewResultCache(0),
		hotSet: NewHotSet(0),
	}
}

// WithRerankModel sets the rerank model to use for the precise-profile pass.
// Can be called after New; not safe to call concurrently with Retrieve.
func (r *Retriever) WithRerankModel(model string) *Retriever {
	r.rerankModel = model
	return r
}

// WithEventCapture wires the event store used for Phase-26 trace capture (the async
// retrieve.query event). No-op when injections (and thus the writer) are not wired.
// Call after construction, before serving; not safe to call concurrently with Retrieve.
func (r *Retriever) WithEventCapture(es store.EventStore) *Retriever {
	if r.injWr != nil && es != nil {
		r.injWr.SetEventStore(es)
	}
	return r
}

// activityTurnsScanCap bounds the per-call record-timestamp fetch for ActivityTurns.
// Beyond this the count saturates (the decay term is already near-floor for memories
// with this many intervening turns), keeping the fetch bounded on the hot path.
const activityTurnsScanCap = 20000

// buildQueryEvent assembles the response-keyed retrieve.query event (Phase 26, D-086).
// SubjectID = response_id so traces.Reconstruct finds it via ListBySubject.
func buildQueryEvent(scope identity.Scope, responseID, query, support string, degraded bool, now int64) *store.Event {
	payload, err := json.Marshal(struct {
		Query    string `json:"query"`
		Support  string `json:"support"`
		Degraded bool   `json:"degraded"`
	}{Query: query, Support: support, Degraded: degraded})
	if err != nil {
		return nil
	}
	return &store.Event{
		ID:        ulid.Make().String(),
		TenantID:  scope.Tenant,
		ProjectID: scope.Project,
		UserID:    scope.User,
		Type:      "retrieve.query",
		SubjectID: responseID,
		Reason:    "retrieve: response query captured for the reasoning trace",
		Payload:   string(payload),
		CreatedAt: now,
	}
}

// Cache returns the result cache, which also implements ScopeInvalidator.
func (r *Retriever) Cache() *ResultCache { return r.cache }

// NewWithInjections creates a Retriever that also records injection rows.
// Close must be called to drain the injection writer on shutdown.
func NewWithInjections(mem store.MemoryStore, recs store.RecordStore, vi vindex.Index, gw gateway.Gateway, injSt store.InjectionStore, log *slog.Logger) *Retriever {
	r := New(mem, recs, vi, gw, log)
	if injSt != nil {
		r.injWr = NewInjectionWriter(injSt, log)
		r.injWr.SetMemoryCounter(mem) // bump inject_count on each injection (D-008)
	}
	return r
}

// Close drains the injection writer goroutine. No-op when injections are not wired.
// Must be called before the program exits to ensure all pending injection rows are written.
func (r *Retriever) Close() {
	if r.injWr != nil {
		r.injWr.Close()
	}
}

// Retrieve runs the four lanes, fuses with RRF, applies utility scoring, and
// returns the top-limit memories. The scope is enforced in every lane's store
// call. Injection rows are recorded asynchronously after the limit trim (D-051).
//
// Phase 15: if a grants store is wired, effective scopes are resolved at the
// start (one extra SQL query; D-060). For multi-scope requests (grants active),
// lanes run across all effective scopes and the result cache is bypassed.
// Zone-ceiling filtering is applied in Go as the defense-in-depth predicate (AC-1).
func (r *Retriever) Retrieve(ctx context.Context, scope identity.Scope, req Request) (*Response, error) {
	// Resolve profile presets for laneK and scoringK.
	prof, ok := profileByName(req.Profile)
	if !ok {
		// Caller validation should have caught this; degrade to balanced.
		prof = ProfileBalanced
	}
	laneK := prof.LaneK

	limit := req.Limit
	if limit <= 0 {
		limit = prof.DefaultLimit
	}
	if limit > maxLimit {
		limit = maxLimit
	}

	// Resolve or generate the response ID (D-051).
	responseID := req.ResponseID
	if responseID == "" {
		responseID = ulid.Make().String()
	}

	// Build the query tokens for the structured lane, hub LRU, and cache key.
	tokens := Tokenize(req.Query)
	querySig := QuerySig(tokens)

	// Phase 15: resolve effective scopes (≤1 extra query; D-060).
	// For the common case (no grants wired, or only own scope), this is a no-op
	// equivalent — effectiveScopes has exactly one element.
	effectiveScopes := r.resolveEffectiveScopes(ctx, store.ScopedQuery{Scope: scope})
	multiScope := len(effectiveScopes) > 1

	// Result-cache check (Phase 12).
	// Skipped for debug:true (breakdowns are not cached — they're one-time diagnostic
	// data and must be freshly computed). Session ID is part of the key because it
	// affects the utility score (write-echo cooldown).
	// Multi-scope requests are NOT cached: revocation must be effective immediately (D-060).
	if !req.Debug && !multiScope {
		if cachedItems, cachedSup, ok := r.cache.Get(scope, querySig, req.Profile, req.SessionID, req.Window.From, req.Window.Until); ok {
			return &Response{
				ResponseID: responseID,
				Items:      cachedItems,
				Support:    cachedSup,
				Degraded:   false,
				API:        "v1",
				CacheHit:   true,
			}, nil
		}
	}

	// Embed the query for the vector lane (may fail → degraded).
	var queryVec []float32
	degraded := false
	if r.gw != nil {
		resp, err := r.gw.Embed(ctx, gateway.EmbedRequest{Inputs: []string{req.Query}})
		if err != nil {
			if errors.Is(err, gateway.ErrGatewayUnavailable) {
				r.log.WarnContext(ctx, "retrieval: gateway unavailable — degraded mode", "err", err)
			} else {
				r.log.WarnContext(ctx, "retrieval: embed failed — degraded mode", "err", err)
			}
			degraded = true
		} else if len(resp.Vectors) > 0 {
			queryVec = resp.Vectors[0]
		}
	} else {
		degraded = true
	}

	// Run all non-vector lanes concurrently plus the vector lane.
	// Phase 15: loops over effectiveScopes (≥1 element). For the common case
	// (single own scope), this is identical to the pre-Phase-15 path.
	eg, egCtx := errgroup.WithContext(ctx)

	var lexicalIDs, queryIDs, structuredIDs, vectorIDs []string

	eg.Go(func() error {
		var all []string
		for _, sq := range effectiveScopes {
			hits, err := r.mem.LexicalSearch(egCtx, sq.Scope, req.Query, laneK, req.Window, req.Kinds)
			if err != nil {
				r.log.WarnContext(egCtx, "retrieval: lexical lane error", "err", err)
				continue // degraded per-lane but not fatal
			}
			for _, h := range hits {
				all = append(all, h.MemoryID)
			}
		}
		lexicalIDs = all
		return nil
	})

	eg.Go(func() error {
		var all []string
		for _, sq := range effectiveScopes {
			hits, err := r.mem.QuerySearch(egCtx, sq.Scope, req.Query, laneK, req.Window)
			if err != nil {
				r.log.WarnContext(egCtx, "retrieval: queries lane error", "err", err)
				continue
			}
			for _, h := range hits {
				all = append(all, h.MemoryID)
			}
		}
		queryIDs = all
		return nil
	})

	eg.Go(func() error {
		if len(tokens) == 0 {
			return nil
		}
		var all []string
		for _, sq := range effectiveScopes {
			neighbors, err := r.mem.FindNeighbors(egCtx, sq.Scope, store.NeighborQuery{
				Entities: tokens,
				Keywords: tokens,
				Kinds:    req.Kinds,
				Limit:    laneK,
			})
			if err != nil {
				r.log.WarnContext(egCtx, "retrieval: structured lane error", "err", err)
				continue
			}
			for _, n := range neighbors {
				all = append(all, n.ID)
			}
		}
		structuredIDs = all
		return nil
	})

	eg.Go(func() error {
		if degraded || queryVec == nil {
			return nil
		}
		var all []string
		for _, sq := range effectiveScopes {
			hits, err := r.vi.Search(egCtx, sq.Scope, queryVec, laneK, vindex.Filter{
				Kinds:  req.Kinds,
				Window: req.Window,
			})
			if err != nil {
				r.log.WarnContext(egCtx, "retrieval: vector lane error", "err", err)
				continue
			}
			for _, h := range hits {
				all = append(all, h.MemoryID)
			}
		}
		vectorIDs = all
		return nil
	})

	if err := eg.Wait(); err != nil {
		return nil, err
	}

	// Build lane map for RRF.
	lanes := make(map[string][]string, 4)
	if len(lexicalIDs) > 0 {
		lanes["lexical"] = lexicalIDs
	}
	if len(queryIDs) > 0 {
		lanes["queries"] = queryIDs
	}
	if len(structuredIDs) > 0 {
		lanes["structured"] = structuredIDs
	}
	if !degraded && len(vectorIDs) > 0 {
		lanes["vector"] = vectorIDs
	}

	// Fuse — use a wider window before scoring to give scoring room to rerank.
	fused := rrf(lanes)

	// Trim to profile.ScoringK before scoring to bound the GetMany call.
	scoringK := prof.ScoringK
	if len(fused) > scoringK {
		fused = fused[:scoringK]
	}

	if len(fused) == 0 {
		return &Response{ResponseID: responseID, Support: Support{Strength: "weak"}, Degraded: degraded, API: "v1"}, nil
	}

	// Bulk-fetch the top memories.
	topIDs := make([]string, len(fused))
	rrfScoreByID := make(map[string]float64, len(fused))
	lanesByID := make(map[string][]string, len(fused))
	for i, h := range fused {
		topIDs[i] = h.MemoryID
		rrfScoreByID[h.MemoryID] = h.Score
		lanesByID[h.MemoryID] = h.Lanes
	}

	// Phase 15: for multi-scope requests, call GetMany for each effective scope
	// and merge. For the own scope (ceiling=""), fetch as today. For granted scopes,
	// apply zone ceiling defense-in-depth after fetching (AC-1).
	var mems []store.Memory
	var err error
	if !multiScope {
		// Common case: single scope — original behavior.
		mems, err = r.mem.GetMany(ctx, scope, topIDs)
		if err != nil {
			return nil, err
		}
	} else {
		seenIDs := make(map[string]bool, len(topIDs))
		for _, sq := range effectiveScopes {
			got, gErr := r.mem.GetMany(ctx, sq.Scope, topIDs)
			if gErr != nil {
				r.log.WarnContext(ctx, "retrieval: GetMany for granted scope failed",
					"scope", sq.Scope.String(), "err", gErr)
				continue
			}
			// Apply zone ceiling (defense-in-depth, AC-1).
			if sq.ZoneCeiling != "" {
				got = applyZoneCeiling(got, sq.ZoneCeiling)
			}
			// Apply the grant's kind/topic slice (D-089): a topic/kind-filtered grant
			// only exposes the owner's matching memories, never the whole scope.
			if sq.KindFilter != "" {
				got = filterByKind(got, sq.KindFilter)
			}
			if sq.TopicFilter != "" {
				got = r.filterByTopic(ctx, sq.Scope, got, sq.TopicFilter)
			}
			for _, m := range got {
				if !seenIDs[m.ID] {
					seenIDs[m.ID] = true
					mems = append(mems, m)
				}
			}
		}
	}

	// Per-item ActivityTurns (D-008 activity-turn decay): fetch the scope's record
	// created_at timestamps newer than the OLDEST item's last_accessed_at ONCE, then
	// count per memory in memory (records after that memory's own last_accessed_at).
	// This gives each item its true turn count without N round-trips — replacing the
	// old single-count-for-all approximation that mis-estimated decay per item.
	var recTimes []int64 // ASC; empty ⇒ activityTurns 0 for all (e.g. recs unwired)
	if r.recs != nil && len(mems) > 0 {
		// Bound the fetch by the oldest POSITIVE last_accessed_at. Never-accessed
		// memories (0) don't decay on the activity axis (scoring's recently-created
		// assumption), so they don't widen the scan.
		var minLastAccessed int64
		for _, m := range mems {
			if m.LastAccessedAt > 0 && (minLastAccessed == 0 || m.LastAccessedAt < minLastAccessed) {
				minLastAccessed = m.LastAccessedAt
			}
		}
		if minLastAccessed > 0 {
			recTimes, err = r.recs.RecordCreatedAtsSince(ctx, scope, minLastAccessed, activityTurnsScanCap)
			if err != nil {
				r.log.WarnContext(ctx, "retrieval: RecordCreatedAtsSince failed — using 0 turns", "err", err)
				recTimes = nil
			}
		}
	}

	nowMs := time.Now().UnixMilli()

	// Record hub signals for all returned memories BEFORE scoring (uses the
	// query signature derived from this retrieve call's tokens).
	for _, m := range mems {
		r.hub.Record(m.ID, querySig)
	}

	// Score each memory and build the response items.
	memByID := make(map[string]store.Memory, len(mems))
	for _, m := range mems {
		memByID[m.ID] = m
	}

	// Convert query window for scoring.
	var scoringWindow *scoring.Window
	if req.Window.From != 0 || req.Window.Until != 0 {
		scoringWindow = &scoring.Window{From: req.Window.From, Until: req.Window.Until}
	}

	type scoredItem struct {
		item  MemoryItem
		score float64
	}
	scored := make([]scoredItem, 0, len(fused))
	for _, h := range fused {
		m, ok := memByID[h.MemoryID]
		if !ok {
			continue // memory disappeared between lane query and bulk fetch
		}

		sameSession := req.SessionID != "" && req.SessionID == m.SessionID

		// Per-item activity turns: count of records created after THIS memory was last
		// accessed (recTimes is ASC, so it's the suffix past last_accessed_at).
		activityTurns := scoring.ActivityTurnsAfter(recTimes, m.LastAccessedAt)

		in := scoring.Inputs{
			Memory: scoring.MemoryFacts{
				MatchCount:     m.MatchCount,
				InjectCount:    m.InjectCount,
				UseCount:       m.UseCount,
				SaveCount:      m.SaveCount,
				FailCount:      m.FailCount,
				NoiseCount:     m.NoiseCount,
				Importance:     m.Importance,
				Confidence:     m.Confidence,
				TrustSource:    m.TrustSource,
				Stability:      m.Stability,
				CreatedAt:      m.CreatedAt,
				LastAccessedAt: m.LastAccessedAt,
				SessionID:      m.SessionID,
			},
			FusedScore:    rrfScoreByID[h.MemoryID],
			Now:           nowMs,
			ActivityTurns: activityTurns,
			QueryWindow:   scoringWindow,
			SameSession:   sameSession,
			HubSignals:    r.hub.Signals(m.ID),
		}

		finalScore, bd := scoring.Score(in)

		// Assign a citation handle (injection ULID) for this item (D-051).
		citationID := ulid.Make().String()

		item := MemoryItem{
			Memory:   m,
			Score:    finalScore,
			Lanes:    nil,
			Citation: citationID,
		}
		if req.IncludeLanes {
			item.Lanes = lanesByID[h.MemoryID]
		}
		if req.Debug {
			bdCopy := bd
			item.Breakdown = &bdCopy
		}
		scored = append(scored, scoredItem{item: item, score: finalScore})
	}

	// Sort by utility score descending, then by MemoryID for determinism.
	sort.Slice(scored, func(i, j int) bool {
		if scored[i].score != scored[j].score {
			return scored[i].score > scored[j].score
		}
		return scored[i].item.Memory.ID < scored[j].item.Memory.ID
	})

	// Trim to requested limit.
	if len(scored) > limit {
		scored = scored[:limit]
	}

	items := make([]MemoryItem, len(scored))
	for i, s := range scored {
		items[i] = s.item
	}

	// Cross-encoder rerank pass (Phase 12) — only for precise profile.
	// On failure, degradedRerank=true and items retain Phase-10 order (D-052).
	var degradedRerank bool
	if prof.EnableRerank && r.gw != nil && len(items) > 0 {
		degradedRerank, items = rerankPass(ctx, r.gw, r.rerankModel, req.Query, items, r.log)
	}

	// Build support summary.
	sup, supErr := buildSupport(ctx, r.mem, scope, items)
	if supErr != nil {
		r.log.WarnContext(ctx, "retrieval: support summary failed", "err", supErr)
		// Non-fatal: return items without conflict detection.
		sup = Support{Strength: "weak", TopScore: 0}
		if len(items) > 0 {
			sup.TopScore = items[0].Score
		}
	}

	// Store result in the cache for subsequent identical queries (Phase 12).
	// Debug requests are not cached (breakdowns are diagnostic and one-time).
	// Multi-scope requests are not cached (revocation must be live, D-060).
	if !req.Debug && !multiScope {
		r.cache.Put(scope, querySig, req.Profile, req.SessionID, req.Window.From, req.Window.Until, items, sup)
	}

	// Feed the hot set with the IDs of injected memories (Phase 12).
	for _, item := range items {
		r.hotSet.Record(scope, item.Memory.ID)
	}

	// Async match_count bumps — non-blocking, errors logged only (P2 spirit).
	// context.Background is intentional: match_count updates are fire-and-forget
	// and must survive the caller's request context being cancelled.
	go func() { //nolint:gosec,contextcheck
		bgCtx := context.Background()
		for _, item := range items {
			if err := r.mem.IncrementCounter(bgCtx, scope, item.Memory.ID, "match"); err != nil {
				r.log.WarnContext(bgCtx, "retrieval: IncrementCounter failed",
					"memory_id", item.Memory.ID, "err", err)
			}
		}
	}()

	// Async injection recording — after limit trim (D-025, D-051).
	// Non-blocking: Enqueue drops the batch if the channel is full.
	if r.injWr != nil {
		injRows := make([]store.Injection, len(items))
		nowInj := time.Now().UnixMilli()
		for i, item := range items {
			injRows[i] = store.Injection{
				ID:         item.Citation,
				ResponseID: responseID,
				MemoryID:   item.Memory.ID,
				Rank:       i,
				Score:      item.Score,
				Lane:       strings.Join(lanesByID[item.Memory.ID], ","),
				CreatedAt:  nowInj,
			}
		}
		// Phase-26 trace capture (D-086): a response-keyed query event so the reasoning
		// trace can include the query (an unbackfillable signal, D-024). Written async
		// by the injection writer (off the retrieve path); nil → the writer skips it
		// when no event store is wired.
		queryEvent := buildQueryEvent(scope, responseID, req.Query, sup.Strength, degraded, nowInj)
		r.injWr.Enqueue(scope, injRows, queryEvent)
	}

	return &Response{
		ResponseID:     responseID,
		Items:          items,
		Support:        sup,
		Degraded:       degraded,
		API:            "v1",
		DegradedRerank: degradedRerank,
	}, nil
}

// SimilarNarratives finds the scope's past episodes most similar to query by
// vector search over narrative memories (Phase 23b, D-082). It embeds query via the
// gateway and searches vindex filtered to kind="narrative", returning the linked
// episode IDs + similarity scores, rank-ordered. Degrades gracefully (D-036): if the
// gateway is absent/unreachable or the embed/search fails, it returns degraded=true
// with no results and NO error — callers fall back to the deterministic episode list.
//
// It satisfies the episodes.NarrativeSearcher interface (no import cycle: retrieval
// does not import episodes). k is the narrative-level top-k (default 5, capped at
// maxLimit); because unlinked/deleted narratives are dropped and duplicate episodes
// deduped after the cut, the episode count returned is best-effort (≤ k).
func (r *Retriever) SimilarNarratives(ctx context.Context, scope identity.Scope, query string, k int) (ids []string, scores []float64, degraded bool, err error) {
	if k <= 0 {
		k = 5
	}
	if k > maxLimit {
		k = maxLimit // resource guard: k is caller-controlled on HTTP/MCP (mirrors Retrieve's clamp)
	}
	if r.gw == nil || r.vi == nil {
		return nil, nil, true, nil
	}
	resp, embErr := r.gw.Embed(ctx, gateway.EmbedRequest{Inputs: []string{query}})
	if embErr != nil || len(resp.Vectors) == 0 {
		r.log.WarnContext(ctx, "episodes/similar: embed failed — degraded", "err", embErr)
		return nil, nil, true, nil
	}
	hits, sErr := r.vi.Search(ctx, scope, resp.Vectors[0], k, vindex.Filter{Kinds: []string{"narrative"}})
	if sErr != nil {
		r.log.WarnContext(ctx, "episodes/similar: vindex search failed — degraded", "err", sErr)
		return nil, nil, true, nil
	}
	seen := make(map[string]struct{}, len(hits))
	for _, h := range hits {
		mem, mErr := r.mem.Get(ctx, scope, h.MemoryID)
		if mErr != nil || mem == nil || mem.EpisodeID == "" {
			continue // narrative deleted or not episode-linked — skip
		}
		if _, dup := seen[mem.EpisodeID]; dup {
			continue // an episode with >1 active narrative — keep the highest-ranked hit
		}
		seen[mem.EpisodeID] = struct{}{}
		ids = append(ids, mem.EpisodeID)
		scores = append(scores, h.Score)
	}
	return ids, scores, false, nil
}
