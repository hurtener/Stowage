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
	"github.com/hurtener/stowage/internal/identity"
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

// filterByKind keeps only memories of the grant's kind (D-089 grant kind_filter).
func filterByKind(mems []store.Memory, kind string) []store.Memory {
	out := make([]store.Memory, 0, len(mems))
	for _, m := range mems {
		if m.Kind == kind {
			out = append(out, m)
		}
	}
	return out
}

// filterByTopic keeps only memories linked to the grant's topic (D-089 grant
// topic_filter), via the memory→topic association. FAILS CLOSED: if topic membership
// cannot be read, the granted scope's memories are dropped rather than over-shared.
func (r *Retriever) filterByTopic(ctx context.Context, scope identity.Scope, mems []store.Memory, topicKey string) []store.Memory {
	if len(mems) == 0 {
		return mems
	}
	ids := make([]string, len(mems))
	for i, m := range mems {
		ids[i] = m.ID
	}
	topicsByID, err := r.mem.MemoriesTopics(ctx, scope, ids)
	if err != nil {
		r.log.WarnContext(ctx, "retrieval: MemoriesTopics failed — dropping granted scope (fail closed)",
			"scope", scope.String(), "err", err)
		return nil
	}
	out := make([]store.Memory, 0, len(mems))
	for _, m := range mems {
		for _, tk := range topicsByID[m.ID] {
			if tk == topicKey {
				out = append(out, m)
				break
			}
		}
	}
	return out
}
