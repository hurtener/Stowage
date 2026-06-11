package retrieval

import (
	"context"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/store"
)

// Strength thresholds for the support summary (RFC §4.2.5, D-027).
// Based on the combined score mass of the top-3 results:
//   - strong:   top-3 mass ≥ strengthStrongThreshold
//   - moderate: top-3 mass ≥ strengthModerateThreshold
//   - weak:     below moderate threshold
//
// These are profile-internal constants; Phase 13 eval re-tunes with data.
const (
	strengthStrongThreshold   = 5.0
	strengthModerateThreshold = 1.5
)

// ConflictPair identifies a contradicts link between two returned memories.
type ConflictPair struct {
	A string `json:"a"` // memory ID
	B string `json:"b"` // memory ID
}

// Support is the per-response evidence summary (RFC §4.2.5, §6c groundwork).
type Support struct {
	// Strength is "weak", "moderate", or "strong" based on top-3 score mass.
	Strength string `json:"strength"`

	// TopScore is the highest utility score in the result set.
	TopScore float64 `json:"top_score"`

	// Conflicts lists contradicts links between pairs in the result set
	// (batch-fetched from ListLinks, one query per retrieve).
	Conflicts []ConflictPair `json:"conflicts,omitempty"`
}

// buildSupport computes the Support summary for a ranked result set.
//
// It calls ListLinks("", "") — one query — to obtain all links for the scope,
// then filters in-memory to (a) type=="contradicts" and (b) both from and to
// are in the result set. This is the "batch, one query" contract from the plan.
func buildSupport(ctx context.Context, mem store.MemoryStore, scope identity.Scope, items []MemoryItem) (Support, error) {
	if len(items) == 0 {
		return Support{Strength: "weak"}, nil
	}

	// Build a set of returned IDs for O(1) lookup.
	inResult := make(map[string]struct{}, len(items))
	for _, it := range items {
		inResult[it.Memory.ID] = struct{}{}
	}

	// Top score.
	topScore := items[0].Score

	// Top-3 score mass.
	mass := 0.0
	for i := 0; i < len(items) && i < 3; i++ {
		mass += items[i].Score
	}

	strength := "weak"
	switch {
	case mass >= strengthStrongThreshold:
		strength = "strong"
	case mass >= strengthModerateThreshold:
		strength = "moderate"
	}

	// Fetch contradicts links — one batch query for the scope.
	links, err := mem.ListLinks(ctx, scope, "", "")
	if err != nil {
		// Log-and-continue: support summary is best-effort (RFC §4.2.5).
		// The caller logs the error; we return an empty conflicts list.
		return Support{Strength: strength, TopScore: topScore}, nil //nolint:nilerr
	}

	var conflicts []ConflictPair
	for _, l := range links {
		if l.Type != "contradicts" {
			continue
		}
		_, fromIn := inResult[l.FromMemory]
		_, toIn := inResult[l.ToMemory]
		if fromIn && toIn {
			conflicts = append(conflicts, ConflictPair{A: l.FromMemory, B: l.ToMemory})
		}
	}

	return Support{
		Strength:  strength,
		TopScore:  topScore,
		Conflicts: conflicts,
	}, nil
}
