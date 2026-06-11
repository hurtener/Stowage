// Package retrieval implements the four-lane retrieval read path (Phase 09,
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
// ActivityTurns approximation (Phase 10): for the scoring decay function, we
// compute a single COUNT of records in scope created after the minimum
// last_accessed_at across all result memories. This count is shared across all
// scored items in one retrieve call to avoid per-item queries. Documented as an
// approximation: a memory whose last_accessed_at is much older than others
// may receive fewer ActivityTurns than it would in a perfect per-item query.
// This is acceptable for Phase 10; a per-item query would be accurate but
// costly.
//
// The response envelope is marked api:"v0" (unstable until Phase 11).
package retrieval

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/hurtener/stowage/internal/gateway"
	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/scoring"
	"github.com/hurtener/stowage/internal/store"
	"github.com/hurtener/stowage/internal/vindex"
)

const (
	// maxLimit is the hard cap on the number of items returned.
	maxLimit = 50

	// laneK is the number of candidates per lane fed into RRF.
	// Larger than maxLimit to give fusion room to rerank.
	laneK = 100
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
	//   2. Future: project-affinity scoring (Phase 11).
	SessionID string
}

// MemoryItem is one retrieval result.
type MemoryItem struct {
	Memory    store.Memory
	Score     float64            // utility-adjusted score (Phase 10; RRF score before Phase 10)
	Lanes     []string           // populated when Request.IncludeLanes is true
	Breakdown *scoring.Breakdown // populated when Request.Debug is true
}

// Response is the retrieve response payload.
type Response struct {
	Items    []MemoryItem
	Support  Support // per-response evidence summary (Phase 10, RFC §4.2.5)
	Degraded bool    // true when the vector lane was skipped (D-036)
	API      string  // "v0" until Phase 11 finalises the envelope
}

// Retriever executes the four-lane retrieval and returns fused + scored results.
// It is safe for concurrent use after New returns.
type Retriever struct {
	mem  store.MemoryStore
	recs store.RecordStore
	vi   vindex.Index
	gw   gateway.Gateway
	hub  *Hub
	log  *slog.Logger
}

// New creates a Retriever wired to the given dependencies.
// recs is used to compute ActivityTurns for the scoring decay function.
func New(mem store.MemoryStore, recs store.RecordStore, vi vindex.Index, gw gateway.Gateway, log *slog.Logger) *Retriever {
	return &Retriever{
		mem:  mem,
		recs: recs,
		vi:   vi,
		gw:   gw,
		hub:  NewHub(hubMaxSize),
		log:  log.With("subsystem", "retrieval"),
	}
}

// Retrieve runs the four lanes, fuses with RRF, applies utility scoring, and
// returns the top-limit memories. The scope is enforced in every lane's store
// call.
func (r *Retriever) Retrieve(ctx context.Context, scope identity.Scope, req Request) (*Response, error) {
	limit := req.Limit
	if limit <= 0 {
		limit = 10
	}
	if limit > maxLimit {
		limit = maxLimit
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

	// Build the query tokens for the structured lane and hub LRU.
	tokens := Tokenize(req.Query)
	querySig := QuerySig(tokens)

	// Run all non-vector lanes concurrently plus the vector lane.
	eg, egCtx := errgroup.WithContext(ctx)

	var lexicalIDs, queryIDs, structuredIDs, vectorIDs []string

	eg.Go(func() error {
		hits, err := r.mem.LexicalSearch(egCtx, scope, req.Query, laneK, req.Window, req.Kinds)
		if err != nil {
			r.log.WarnContext(egCtx, "retrieval: lexical lane error", "err", err)
			return nil // degraded per-lane but not fatal
		}
		ids := make([]string, len(hits))
		for i, h := range hits {
			ids[i] = h.MemoryID
		}
		lexicalIDs = ids
		return nil
	})

	eg.Go(func() error {
		hits, err := r.mem.QuerySearch(egCtx, scope, req.Query, laneK, req.Window)
		if err != nil {
			r.log.WarnContext(egCtx, "retrieval: queries lane error", "err", err)
			return nil
		}
		ids := make([]string, len(hits))
		for i, h := range hits {
			ids[i] = h.MemoryID
		}
		queryIDs = ids
		return nil
	})

	eg.Go(func() error {
		if len(tokens) == 0 {
			return nil
		}
		neighbors, err := r.mem.FindNeighbors(egCtx, scope, store.NeighborQuery{
			Entities: tokens,
			Keywords: tokens,
			Kinds:    req.Kinds,
			Limit:    laneK,
		})
		if err != nil {
			r.log.WarnContext(egCtx, "retrieval: structured lane error", "err", err)
			return nil
		}
		ids := make([]string, len(neighbors))
		for i, n := range neighbors {
			ids[i] = n.ID
		}
		structuredIDs = ids
		return nil
	})

	eg.Go(func() error {
		if degraded || queryVec == nil {
			return nil
		}
		hits, err := r.vi.Search(egCtx, scope, queryVec, laneK, vindex.Filter{
			Kinds:  req.Kinds,
			Window: req.Window,
		})
		if err != nil {
			r.log.WarnContext(egCtx, "retrieval: vector lane error", "err", err)
			return nil
		}
		ids := make([]string, len(hits))
		for i, h := range hits {
			ids[i] = h.MemoryID
		}
		vectorIDs = ids
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

	// Trim to laneK before scoring to bound the GetMany call; scoring may
	// reorder items but we only expose at most limit results.
	scoringK := limit * 2
	if scoringK > laneK {
		scoringK = laneK
	}
	if len(fused) > scoringK {
		fused = fused[:scoringK]
	}

	if len(fused) == 0 {
		return &Response{Support: Support{Strength: "weak"}, Degraded: degraded, API: "v0"}, nil
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

	mems, err := r.mem.GetMany(ctx, scope, topIDs)
	if err != nil {
		return nil, err
	}

	// Compute ActivityTurns approximation:
	// Use the minimum last_accessed_at across all results as the sinceMs bound.
	// This is an approximation: all items receive the same ActivityTurns count.
	// A memory accessed more recently will have its decay underestimated (fewer
	// turns counted), and one accessed less recently will be overestimated.
	// Documented per the phase-10 plan; the trade-off is one query vs N queries.
	var activityTurns int64
	if r.recs != nil && len(mems) > 0 {
		minLastAccessed := mems[0].LastAccessedAt
		for _, m := range mems[1:] {
			if m.LastAccessedAt < minLastAccessed {
				minLastAccessed = m.LastAccessedAt
			}
		}
		if minLastAccessed > 0 {
			activityTurns, err = r.recs.CountRecordsSince(ctx, scope, minLastAccessed)
			if err != nil {
				r.log.WarnContext(ctx, "retrieval: CountRecordsSince failed — using 0 turns", "err", err)
				activityTurns = 0
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

		item := MemoryItem{
			Memory: m,
			Score:  finalScore,
			Lanes:  nil,
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

	return &Response{Items: items, Support: sup, Degraded: degraded, API: "v0"}, nil
}
