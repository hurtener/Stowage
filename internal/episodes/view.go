package episodes

import (
	"context"
	"errors"
	"fmt"

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
	Narrative         string // the narrative memory's content ("" if not yet narrated)
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
