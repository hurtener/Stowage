package proactive

import (
	"context"

	"github.com/hurtener/stowage/internal/episodes"
	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/store"
)

// Candidate is a pre-scoring proactive offer produced by a trigger rule.
type Candidate struct {
	TriggerKind string
	MemoryID    string  // the offered memory (the narrative memory for episode triggers)
	EpisodeID   string  // set for episode triggers
	Relevance   float64 // pre-utility relevance in [0,1] (recency / similarity / urgency)
	Title       string  // human-facing label
}

// recentWindowMs / expiringWindowMs / candidateScan bound the rule scans.
const (
	recentEpisodeMaxAgeMs = int64(7 * 24 * 60 * 60 * 1000) // an episode "recent" within 7 days
	expiringWindowMs      = int64(3 * 24 * 60 * 60 * 1000) // a memory "expiring" within 3 days
	ruleCandidateCap      = 5                              // per-rule candidate cap before scoring
	decayScanLimit        = 200                            // bounded scan for the expiring rule
)

// recentEpisodeCandidates offers the scope's most recent narrated episode(s) when they
// ended within the recency window — "before we start — this is the Q2 plan from last
// quarter." Gateway-free.
func recentEpisodeCandidates(ctx context.Context, st store.Store, scope identity.Scope, now int64) ([]Candidate, error) {
	res, err := episodes.List(ctx, st, scope, episodes.ListOptions{Limit: ruleCandidateCap})
	if err != nil {
		return nil, err
	}
	out := make([]Candidate, 0, len(res.Episodes))
	for _, ep := range res.Episodes {
		if ep.NarrativeMemoryID == "" {
			continue // not yet narrated — nothing to offer
		}
		age := now - ep.EndedAt
		if age < 0 || age > recentEpisodeMaxAgeMs {
			continue
		}
		// Recency relevance: 1 at now, →0 at the window edge.
		rel := 1 - float64(age)/float64(recentEpisodeMaxAgeMs)
		out = append(out, Candidate{
			TriggerKind: ClassRecentEpisode, MemoryID: ep.NarrativeMemoryID, EpisodeID: ep.ID,
			Relevance: rel, Title: ep.Title,
		})
	}
	return out, nil
}

// similarEpisodeCandidates offers the past episode whose narrative most resembles the
// query — "this looks like the migration you did in March." Gateway-dependent
// (embeds the query); degraded=true ⇒ the caller skips this class.
func similarEpisodeCandidates(ctx context.Context, st store.Store, searcher episodes.NarrativeSearcher, scope identity.Scope, query string) ([]Candidate, bool, error) {
	if query == "" || searcher == nil {
		return nil, false, nil
	}
	views, degraded, err := episodes.Similar(ctx, st, searcher, scope, query, ruleCandidateCap)
	if err != nil {
		return nil, degraded, err
	}
	out := make([]Candidate, 0, len(views))
	for _, ep := range views {
		if ep.NarrativeMemoryID == "" {
			continue
		}
		out = append(out, Candidate{
			TriggerKind: ClassSimilarEpisode, MemoryID: ep.NarrativeMemoryID, EpisodeID: ep.ID,
			Relevance: clamp01(ep.Score), Title: ep.Title,
		})
	}
	return out, degraded, nil
}

// expiringCandidates offers active memories approaching their valid_until — "this
// note expires tomorrow; still true?" Gateway-free. Scans a bounded page of active
// memories and keeps those expiring within the window. urgency relevance: closer to
// expiry ⇒ higher.
func expiringCandidates(ctx context.Context, st store.Store, scope identity.Scope, now int64) ([]Candidate, error) {
	mems, _, err := st.Memories().ListActiveForDecay(ctx, scope, decayScanLimit, "")
	if err != nil {
		return nil, err
	}
	out := make([]Candidate, 0)
	horizon := now + expiringWindowMs
	for _, m := range mems {
		if m.ValidUntil <= 0 || m.ValidUntil <= now || m.ValidUntil > horizon {
			continue // no expiry set, already expired, or not yet within the window
		}
		// Urgency: 1 at expiry, →0 at the far edge of the window.
		rel := 1 - float64(m.ValidUntil-now)/float64(expiringWindowMs)
		out = append(out, Candidate{
			TriggerKind: ClassExpiring, MemoryID: m.ID, Relevance: clamp01(rel), Title: truncate(m.Content, 64),
		})
		if len(out) >= ruleCandidateCap {
			break
		}
	}
	return out, nil
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
