package retrieval

import "sort"

// rrfK is the reciprocal rank fusion constant (standard RRF value = 60).
// Not a config knob (D-034 guardrail; eval data informs changes via D-035).
const rrfK = 60

// FusedHit is a result from RRF fusion over multiple lanes.
type FusedHit struct {
	MemoryID string
	Score    float64  // RRF fused score; higher = more relevant
	Lanes    []string // lane names that contributed this result
}

// rrf performs Reciprocal Rank Fusion over a set of ranked lane results.
// lanes maps lane name → ordered list of MemoryIDs (best to worst).
// Returns hits sorted by fused score descending.
//
// RRF formula (Cormack et al., 2009):
//
//	score(d) = Σ_l 1 / (k + rank_l(d))
//
// where rank is 1-indexed (rank=1 for the top result in lane l).
func rrf(lanes map[string][]string) []FusedHit {
	scores := make(map[string]float64)
	provenance := make(map[string][]string)

	for lane, ids := range lanes {
		for rank, id := range ids {
			scores[id] += 1.0 / float64(rrfK+rank+1) // rank is 0-indexed here
			provenance[id] = append(provenance[id], lane)
		}
	}

	hits := make([]FusedHit, 0, len(scores))
	for id, score := range scores {
		hits = append(hits, FusedHit{
			MemoryID: id,
			Score:    score,
			Lanes:    dedupLanes(provenance[id]),
		})
	}

	// Sort by score descending, then by MemoryID for deterministic output.
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		return hits[i].MemoryID < hits[j].MemoryID
	})

	return hits
}

func dedupLanes(lanes []string) []string {
	seen := make(map[string]bool, len(lanes))
	out := make([]string, 0, len(lanes))
	for _, l := range lanes {
		if !seen[l] {
			seen[l] = true
			out = append(out, l)
		}
	}
	return out
}
