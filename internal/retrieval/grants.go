package retrieval

// grants.go — grant-aware retrieval integration (Phase 15, D-060).
//
// When a GrantStore is wired via SetGrants, Retrieve resolves EffectiveScopes
// at the start of each request (one SQL JOIN query) and fans out the four lane
// queries across all effective scopes. Zone ceiling is enforced in Go after
// GetMany (defense-in-depth, AC-1). The result-cache is bypassed for multi-
// scope requests (grant set may differ per-request; revocation must be live).
//
// Hot-path regression guard: when no grants exist (common case), effective
// scopes resolves to a single-element slice in ≤1 query, and the rest of the
// Retrieve path is identical to today.

import (
	"context"
	"log/slog"

	"github.com/hurtener/stowage/internal/grants"
	"github.com/hurtener/stowage/internal/store"
)

// SetGrants wires the grant store for per-request EffectiveScopes resolution.
// May be called after New; not safe to call concurrently with Retrieve.
func (r *Retriever) SetGrants(gs store.GrantStore) {
	r.grantsSt = gs
}

// resolveEffectiveScopes returns the set of scopes the caller may read.
// If no grant store is wired or EffectiveScopes fails, returns a single-element
// slice containing the caller's own scope (fail-safe).
func (r *Retriever) resolveEffectiveScopes(ctx context.Context, scope store.ScopedQuery) []store.ScopedQuery {
	if r.grantsSt == nil {
		return []store.ScopedQuery{scope}
	}
	scopes, err := r.grantsSt.EffectiveScopes(ctx, scope.Scope)
	if err != nil {
		r.log.WarnContext(ctx, "retrieval: EffectiveScopes failed — using own scope only",
			slog.Any("err", err))
		return []store.ScopedQuery{scope}
	}
	return scopes
}

// applyZoneCeiling filters memories that exceed the zone ceiling for granted
// scopes. Own-scope memories (ceiling="") are never filtered.
// This is the defense-in-depth predicate (AC-1): personal/intimate rows never
// cross a grant even if mis-stored.
func applyZoneCeiling(mems []store.Memory, ceiling string) []store.Memory {
	return grants.ApplyCeiling(mems, ceiling)
}
