// Package retrieval implements the four-lane retrieval read path (Phase 09,
// RFC §4.2). Lanes run concurrently via errgroup; results are fused by RRF.
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
// The response envelope is marked api:"v0" (unstable until Phase 11).
package retrieval

import (
	"context"
	"errors"
	"log/slog"

	"golang.org/x/sync/errgroup"

	"github.com/hurtener/stowage/internal/gateway"
	"github.com/hurtener/stowage/internal/identity"
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
}

// MemoryItem is one retrieval result.
type MemoryItem struct {
	Memory store.Memory
	Score  float64  // RRF fused score
	Lanes  []string // populated when Request.IncludeLanes is true
}

// Response is the retrieve response payload.
type Response struct {
	Items    []MemoryItem
	Degraded bool   // true when the vector lane was skipped (D-036)
	API      string // "v0" until Phase 11 finalises the envelope
}

// Retriever executes the four-lane retrieval and returns fused results.
// It is safe for concurrent use after New returns.
type Retriever struct {
	mem store.MemoryStore
	vi  vindex.Index
	gw  gateway.Gateway
	log *slog.Logger
}

// New creates a Retriever wired to the given dependencies.
func New(mem store.MemoryStore, vi vindex.Index, gw gateway.Gateway, log *slog.Logger) *Retriever {
	return &Retriever{
		mem: mem,
		vi:  vi,
		gw:  gw,
		log: log.With("subsystem", "retrieval"),
	}
}

// Retrieve runs the four lanes, fuses with RRF, and returns the top-limit
// memories. The scope is enforced in every lane's store call.
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

	// Build the query tokens for the structured lane.
	tokens := Tokenize(req.Query)

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

	// Fuse and trim.
	fused := rrf(lanes)
	if len(fused) > limit {
		fused = fused[:limit]
	}

	if len(fused) == 0 {
		return &Response{Degraded: degraded, API: "v0"}, nil
	}

	// Bulk-fetch the top memories.
	topIDs := make([]string, len(fused))
	scoreByID := make(map[string]float64, len(fused))
	lanesByID := make(map[string][]string, len(fused))
	for i, h := range fused {
		topIDs[i] = h.MemoryID
		scoreByID[h.MemoryID] = h.Score
		lanesByID[h.MemoryID] = h.Lanes
	}

	mems, err := r.mem.GetMany(ctx, scope, topIDs)
	if err != nil {
		return nil, err
	}

	// Build response items in fused order.
	memByID := make(map[string]store.Memory, len(mems))
	for _, m := range mems {
		memByID[m.ID] = m
	}

	items := make([]MemoryItem, 0, len(fused))
	for _, h := range fused {
		m, ok := memByID[h.MemoryID]
		if !ok {
			continue // memory disappeared between lane query and bulk fetch
		}
		item := MemoryItem{
			Memory: m,
			Score:  scoreByID[h.MemoryID],
		}
		if req.IncludeLanes {
			item.Lanes = lanesByID[h.MemoryID]
		}
		items = append(items, item)
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

	return &Response{Items: items, Degraded: degraded, API: "v0"}, nil
}
