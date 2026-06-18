package episodes

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/store"
)

// view.go is the Phase-23 episodic-retrieval read core (RFC §6b, D-080): the
// deterministic, gateway-free assembly the HTTP/MCP/SDK surfaces all call (mirrors
// playbook.Assemble). Similar-episode contrast + gateway synthesis are Phase 23b.

// EpisodeView is one episode plus its narrative, wire-neutral for the surfaces.
type EpisodeView struct {
	ID                string
	SessionID         string
	Title             string
	Status            string
	Outcome           string
	StartedAt         int64
	EndedAt           int64
	NarrativeMemoryID string
	Narrative         string  // the narrative memory's content ("" if not yet narrated)
	Score             float64 // similarity score (similar-episode path only; Phase 23b)
}

// NarrativeSearcher finds episodes whose narrative is most similar to a query
// (satisfied by *retrieval.Retriever). Returns parallel episode-id + score slices,
// rank-ordered, with degraded=true (no error) when the gateway/vindex can't serve.
type NarrativeSearcher interface {
	SimilarNarratives(ctx context.Context, scope identity.Scope, query string, k int) (ids []string, scores []float64, degraded bool, err error)
}

// Similar returns the scope's episodes most similar to query (Phase 23b, D-082),
// rank-ordered with their similarity Score + narrative — the §6b contrast material.
// degraded mirrors the searcher (callers fall back to the deterministic list).
func Similar(ctx context.Context, st store.Store, searcher NarrativeSearcher, scope identity.Scope, query string, k int) ([]EpisodeView, bool, error) {
	ids, scores, degraded, err := searcher.SimilarNarratives(ctx, scope, query, k)
	if err != nil {
		return nil, degraded, fmt.Errorf("episodes: similar: %w", err)
	}
	views := make([]EpisodeView, 0, len(ids))
	for i, id := range ids {
		ep, gerr := st.Episodes().GetEpisode(ctx, scope, id)
		if errors.Is(gerr, store.ErrNotFound) {
			continue // episode gone — skip, preserve rank of the rest
		}
		if gerr != nil {
			return nil, degraded, fmt.Errorf("episodes: similar load %s: %w", id, gerr)
		}
		v, terr := toView(ctx, st, scope, *ep)
		if terr != nil {
			return nil, degraded, terr
		}
		if i < len(scores) { // defensive: never index out of a misbehaving searcher's scores (no panic across the core boundary)
			v.Score = scores[i]
		}
		views = append(views, v)
	}
	return views, degraded, nil
}

// maxArcNodes caps an arc traversal (resource guard).
const maxArcNodes = 200

// Arc returns the episodes threaded to episodeID — its cross-session arc (Phase 24b,
// D-081), including the seed, most-recent-first. It walks relates_to edges between the
// episodes' narrative memories (BFS, cycle-safe, capped) and maps each connected
// narrative back to its episode. Deterministic and gateway-free. An absent seed ⇒
// empty; an unarrated/unthreaded seed ⇒ just the seed.
func Arc(ctx context.Context, st store.Store, scope identity.Scope, episodeID string) ([]EpisodeView, error) {
	seed, err := st.Episodes().GetEpisode(ctx, scope, episodeID)
	if errors.Is(err, store.ErrNotFound) {
		return []EpisodeView{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("episodes: arc: get seed: %w", err)
	}

	// Collect episode ids in the arc, starting with the seed. BFS over relates_to
	// edges between narrative memories.
	epIDs := []string{seed.ID}
	seenEp := map[string]bool{seed.ID: true}

	if seed.NarrativeMemoryID != "" {
		seenNarr := map[string]bool{seed.NarrativeMemoryID: true}
		queue := []string{seed.NarrativeMemoryID}
		for len(queue) > 0 && len(epIDs) < maxArcNodes {
			narrID := queue[0]
			queue = queue[1:]
			neighbors, lerr := relatedNarratives(ctx, st, scope, narrID)
			if lerr != nil {
				return nil, lerr
			}
			for _, nID := range neighbors {
				if seenNarr[nID] {
					continue
				}
				seenNarr[nID] = true
				mem, gerr := st.Memories().Get(ctx, scope, nID)
				if gerr != nil || mem == nil || mem.Status != "active" || mem.EpisodeID == "" {
					continue // narrative gone/non-active/unlinked — skip
				}
				if !seenEp[mem.EpisodeID] {
					seenEp[mem.EpisodeID] = true
					epIDs = append(epIDs, mem.EpisodeID)
					if len(epIDs) >= maxArcNodes {
						break // cap enforced per-append, not just per-BFS-level
					}
				}
				queue = append(queue, nID)
			}
		}
	}

	// Load each episode + narrative; then sort most-recent-first.
	views := make([]EpisodeView, 0, len(epIDs))
	for _, id := range epIDs {
		ep, gerr := st.Episodes().GetEpisode(ctx, scope, id)
		if errors.Is(gerr, store.ErrNotFound) {
			continue
		}
		if gerr != nil {
			return nil, fmt.Errorf("episodes: arc: load %s: %w", id, gerr)
		}
		v, terr := toView(ctx, st, scope, *ep)
		if terr != nil {
			return nil, terr
		}
		views = append(views, v)
	}
	sort.SliceStable(views, func(i, j int) bool {
		if views[i].StartedAt != views[j].StartedAt {
			return views[i].StartedAt > views[j].StartedAt
		}
		return views[i].ID > views[j].ID
	})
	return views, nil
}

// relatedNarratives returns the narrative memory ids linked to narrID by a relates_to
// edge in either direction.
func relatedNarratives(ctx context.Context, st store.Store, scope identity.Scope, narrID string) ([]string, error) {
	fromLinks, err := st.Memories().ListLinks(ctx, scope, narrID, "")
	if err != nil {
		return nil, fmt.Errorf("episodes: arc: list links from %s: %w", narrID, err)
	}
	toLinks, err := st.Memories().ListLinks(ctx, scope, "", narrID)
	if err != nil {
		return nil, fmt.Errorf("episodes: arc: list links to %s: %w", narrID, err)
	}
	var out []string
	for _, l := range fromLinks {
		if l.Type == "relates_to" && l.ToMemory != narrID {
			out = append(out, l.ToMemory)
		}
	}
	for _, l := range toLinks {
		if l.Type == "relates_to" && l.FromMemory != narrID {
			out = append(out, l.FromMemory)
		}
	}
	return out, nil
}

// ListOptions filters/paginates an episode list.
type ListOptions struct {
	Limit     int
	Cursor    string
	From      int64  // 0 = unbounded; episodes ending before From are excluded
	Until     int64  // 0 = unbounded; episodes starting after Until are excluded
	SessionID string // "" = any session
}

func (o ListOptions) filtered() bool { return o.From > 0 || o.Until > 0 || o.SessionID != "" }

// ListResult is a page of episode views.
type ListResult struct {
	Episodes   []EpisodeView
	NextCursor string
}

// defaultLimit / maxScanPages bound the work.
const (
	defaultLimit = 20
	maxLimit     = 100 // clamp caller-supplied limits (resource guard; mirrors retrieval)
	maxScanPages = 50
)

// List returns the scope's episodes most-recent-first with their narratives.
// Unfiltered, it paginates exactly via the store cursor. With a window/session
// filter it scans (bounded) and returns the matched set (a window is a bounded
// period — the §6b structured summary), with an empty NextCursor.
func List(ctx context.Context, st store.Store, scope identity.Scope, opts ListOptions) (ListResult, error) {
	limit := opts.Limit
	if limit <= 0 {
		limit = defaultLimit
	}
	if limit > maxLimit {
		limit = maxLimit
	}
	if !opts.filtered() {
		eps, next, err := st.Episodes().ListEpisodes(ctx, scope, limit, opts.Cursor)
		if err != nil {
			return ListResult{}, fmt.Errorf("episodes: list: %w", err)
		}
		views, err := toViews(ctx, st, scope, eps)
		if err != nil {
			return ListResult{}, err
		}
		return ListResult{Episodes: views, NextCursor: next}, nil
	}

	// Filtered: scan pages until we have `limit` matches or run out / hit the cap.
	var matched []store.Episode
	cursor := opts.Cursor
	for page := 0; len(matched) < limit && page < maxScanPages; page++ {
		eps, next, err := st.Episodes().ListEpisodes(ctx, scope, limit, cursor)
		if err != nil {
			return ListResult{}, fmt.Errorf("episodes: list (filtered): %w", err)
		}
		for _, ep := range eps {
			if matchesWindow(ep, opts) {
				matched = append(matched, ep)
				if len(matched) == limit {
					break
				}
			}
		}
		if next == "" {
			break
		}
		cursor = next
	}
	views, err := toViews(ctx, st, scope, matched)
	if err != nil {
		return ListResult{}, err
	}
	return ListResult{Episodes: views, NextCursor: ""}, nil
}

// matchesWindow reports whether an episode falls within the filter. The window is
// an overlap test: an episode is in [From,Until] if it is not entirely before From
// nor entirely after Until.
func matchesWindow(ep store.Episode, opts ListOptions) bool {
	if opts.SessionID != "" && ep.SessionID != opts.SessionID {
		return false
	}
	if opts.From > 0 && ep.EndedAt < opts.From {
		return false
	}
	if opts.Until > 0 && ep.StartedAt > opts.Until {
		return false
	}
	return true
}

// Get returns one episode + its narrative. ErrNotFound (from the store) when absent.
func Get(ctx context.Context, st store.Store, scope identity.Scope, id string) (*EpisodeView, error) {
	ep, err := st.Episodes().GetEpisode(ctx, scope, id)
	if err != nil {
		return nil, err
	}
	v, err := toView(ctx, st, scope, *ep)
	if err != nil {
		return nil, err
	}
	return &v, nil
}

func toViews(ctx context.Context, st store.Store, scope identity.Scope, eps []store.Episode) ([]EpisodeView, error) {
	out := make([]EpisodeView, 0, len(eps))
	for _, ep := range eps {
		v, err := toView(ctx, st, scope, ep)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

func toView(ctx context.Context, st store.Store, scope identity.Scope, ep store.Episode) (EpisodeView, error) {
	v := EpisodeView{
		ID: ep.ID, SessionID: ep.SessionID, Title: ep.Title, Status: ep.Status,
		Outcome: ep.Outcome, StartedAt: ep.StartedAt, EndedAt: ep.EndedAt,
		NarrativeMemoryID: ep.NarrativeMemoryID,
	}
	if ep.NarrativeMemoryID != "" {
		mem, err := st.Memories().Get(ctx, scope, ep.NarrativeMemoryID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return EpisodeView{}, fmt.Errorf("episodes: load narrative %s: %w", ep.NarrativeMemoryID, err)
		}
		if mem != nil {
			v.Narrative = mem.Content
		}
	}
	return v, nil
}
