package retrieval

// topicfilter.go — the own-scope request-level topic filter (Phase ae6, D-144).
//
// filterByTopicOwnScope is deliberately DISTINCT from grants.go's filterByTopic:
// the two share a shape (a discrete MemoriesTopics batch read gating a candidate
// set) but have INTENTIONALLY OPPOSITE error semantics (D-139):
//
//   - grants' filterByTopic (cross-scope sharing) fails CLOSED — a MemoriesTopics
//     error drops the whole granted scope, because over-sharing must never happen.
//   - filterByTopicOwnScope (own-scope curation) fails OPEN — a MemoriesTopics
//     error returns the caller's own unfiltered candidates, degraded=true, because
//     this filter only ever narrows the caller's OWN results; it is a relevance/
//     curation lens, not a P3 isolation boundary, so "fail safe" here means "don't
//     silently under-return the caller's own memories."
//
// Do not merge these two functions — the divergence is intentional and load-
// bearing (see docs/decisions.md D-139).

import (
	"context"

	"github.com/hurtener/stowage/internal/identity"
)

// filterByTopicOwnScope keeps only the caller's OWN-scope candidate ids whose
// memory_topics membership satisfies include/exclude, via the scope-required
// MemoriesTopics batch read. An id is kept iff (include is empty OR its topics
// intersect include) AND its topics do not intersect exclude. Order is not
// guaranteed; callers that need order-preservation project the result back
// against their own ordered slice (as Retrieve does).
//
// FAILS OPEN (D-139): on a MemoriesTopics error it logs a warning and returns the
// input ids UNCHANGED with degraded=true — the caller's own full candidate set,
// never narrowed by a failed read. This is the deliberate opposite of grants'
// fail-CLOSED filterByTopic (which returns nil to never over-share across a grant).
func (r *Retriever) filterByTopicOwnScope(
	ctx context.Context, scope identity.Scope, ids []string, include, exclude []string,
) (kept []string, degraded bool) {
	if len(ids) == 0 {
		return ids, false
	}
	if len(include) == 0 && len(exclude) == 0 {
		return ids, false // no constraint — pass through untouched (additive no-op)
	}

	topicsByID, err := r.mem.MemoriesTopics(ctx, scope, ids)
	if err != nil {
		r.log.WarnContext(ctx, "retrieval: MemoriesTopics failed — failing OPEN on the own-scope topic filter (D-139)",
			"scope", scope.String(), "err", err)
		return ids, true
	}

	includeSet := toSet(include)
	excludeSet := toSet(exclude)

	out := make([]string, 0, len(ids))
	for _, id := range ids {
		topics := topicsByID[id]
		if len(includeSet) > 0 && !anyIn(topics, includeSet) {
			continue
		}
		if len(excludeSet) > 0 && anyIn(topics, excludeSet) {
			continue
		}
		out = append(out, id)
	}
	return out, false
}

// toSet builds a membership set from a topic-key slice.
func toSet(keys []string) map[string]bool {
	if len(keys) == 0 {
		return nil
	}
	s := make(map[string]bool, len(keys))
	for _, k := range keys {
		s[k] = true
	}
	return s
}

// anyIn reports whether any of topics is a member of set.
func anyIn(topics []string, set map[string]bool) bool {
	for _, t := range topics {
		if set[t] {
			return true
		}
	}
	return false
}
