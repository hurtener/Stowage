package retrieval

import (
	"context"
	"fmt"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/store"
)

// browse.go is the Phase-ae5 (D-143) list/browse core: a deterministic,
// gateway-free scoped walk over a scope's memories. Modeled on
// internal/episodes/view.go's List (§9.1-9.5, D-067/D-073): ONE core, thin
// surfaces. It imports no gateway package and constructs no provider request
// (P5 trivially; D-036 trivially — Browse serves in the degraded path).

// BrowseMode selects which ordered scan Browse runs. It is a two-value
// call-site argument, NOT a config knob (mirrors ae3's RenderMode discipline,
// D-141).
type BrowseMode int

const (
	// BrowseRecent walks the scope's memories most-recent-first
	// (created_at DESC) via the new Store.ListByScopeRecent (ae5, D-143).
	BrowseRecent BrowseMode = iota
	// BrowseSuperseded walks the scope's superseded memories via the
	// EXISTING Store.ListByStatus(scope, "superseded", …) — created_at
	// ASCENDING (oldest-first). H4: no new superseded query is added; the
	// ordering asymmetry with BrowseRecent is deliberate and documented
	// (D-143).
	BrowseSuperseded
)

// browseMaxLimit is the hard page-size cap (resource guard, mirrors
// episodes.maxLimit).
const browseMaxLimit = 100

// BrowseOptions parameterises one Browse page. DefaultLimit is the
// config-resolved page size (retrieval.browse_default_limit) used when
// Limit <= 0 — the surface passes its resolved config value so the knob is
// read in ONE core call, not duplicated across three surfaces.
type BrowseOptions struct {
	Mode         BrowseMode
	Limit        int
	Cursor       string
	DefaultLimit int
}

// BrowseResult is one page of memories plus the opaque next cursor
// ("" = last page).
type BrowseResult struct {
	Memories   []store.Memory
	NextCursor string
}

// ParseBrowseMode maps the wire-level mode string to a BrowseMode. "" and
// "recent" both resolve to BrowseRecent (the default); "superseded" resolves
// to BrowseSuperseded. mode is a CLOSED enum — any other value is rejected,
// never silently defaulted (AC-7). All three surfaces call this so the enum
// validation lives in the one core, not duplicated per surface (D-067/D-073).
func ParseBrowseMode(mode string) (BrowseMode, error) {
	switch mode {
	case "", "recent":
		return BrowseRecent, nil
	case "superseded":
		return BrowseSuperseded, nil
	default:
		return BrowseRecent, fmt.Errorf("retrieval: browse: unknown mode %q (want \"recent\" or \"superseded\")", mode)
	}
}

// Browse walks a scope's memories. Scope-required (P3): it delegates only to
// scope-required store queries; there is no unscoped path. Gateway-free
// (D-036) — Browse imports no gateway package.
func Browse(ctx context.Context, st store.Store, scope identity.Scope, opts BrowseOptions) (BrowseResult, error) {
	limit := opts.Limit
	if limit <= 0 {
		limit = opts.DefaultLimit
	}
	if limit <= 0 || limit > browseMaxLimit {
		limit = browseMaxLimit // clamp (also floors a mis-set/zero default)
	}

	switch opts.Mode {
	case BrowseSuperseded:
		// H4: REUSE the existing status query verbatim — no new superseded
		// method is added to the seam or either driver.
		mems, next, err := st.Memories().ListByStatus(ctx, scope, "superseded", limit, opts.Cursor)
		if err != nil {
			return BrowseResult{}, fmt.Errorf("retrieval: browse superseded: %w", err)
		}
		return BrowseResult{Memories: mems, NextCursor: next}, nil
	default: // BrowseRecent
		mems, next, err := st.Memories().ListByScopeRecent(ctx, scope, limit, opts.Cursor)
		if err != nil {
			return BrowseResult{}, fmt.Errorf("retrieval: browse recent: %w", err)
		}
		return BrowseResult{Memories: mems, NextCursor: next}, nil
	}
}
