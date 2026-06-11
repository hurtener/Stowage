package reconcile

import (
	"fmt"
	"strings"

	"github.com/hurtener/stowage/internal/pipeline"
	"github.com/hurtener/stowage/internal/store"
)

// BuildSystemPrompt returns the system prompt for the reconciliation decision call.
// It is stable between calls so callers can use it in golden tests.
func BuildSystemPrompt() string {
	return `You are a memory reconciliation engine for Stowage, an agentic memory system.

Your task is to decide how a new candidate memory should be integrated with existing memories.

Given a candidate memory and zero or more existing neighbor memories that overlap in entities or keywords, choose one of:

- "add"       — The candidate is new information; create a fresh memory.
- "update"    — The candidate refines an existing memory (same subject, improved detail). Supply the target_id of the memory to update.
- "merge"     — Two or more existing memories can be unified under the candidate. Supply all target_ids.
- "supersede" — The candidate directly contradicts an existing memory. Supply the target_id to retire.
- "discard"   — The candidate is redundant, trivial, or noise; do not persist it.
- "park"      — The candidate is uncertain; flag for human review (pending_confirmation).

Rules:
1. Prefer "add" when no neighbor shares the same subject or claim.
2. Prefer "update" when a neighbor covers the same subject but with less detail.
3. Prefer "supersede" when a neighbor states something the candidate directly contradicts.
4. Prefer "merge" only when two or more neighbors are fragments of the same fact.
5. Use "discard" for greetings, filler, or exact re-statements.
6. Use "park" only when you have genuine uncertainty about correctness.

Respond with a JSON object matching the schema. The "reason" field should be a concise one-sentence explanation.`
}

// BuildUserPrompt returns the user-turn prompt for one candidate + its neighbors.
func BuildUserPrompt(c pipeline.Candidate, neighbors []store.Memory) string {
	var b strings.Builder

	fmt.Fprintf(&b, "## Candidate memory\n\n")
	fmt.Fprintf(&b, "**Kind:** %s\n", c.Kind)
	fmt.Fprintf(&b, "**Content:** %s\n", c.Content)
	if c.Context != "" {
		fmt.Fprintf(&b, "**Context:** %s\n", c.Context)
	}
	if len(c.Entities) > 0 {
		fmt.Fprintf(&b, "**Entities:** %s\n", strings.Join(c.Entities, ", "))
	}
	if len(c.Keywords) > 0 {
		fmt.Fprintf(&b, "**Keywords:** %s\n", strings.Join(c.Keywords, ", "))
	}
	fmt.Fprintf(&b, "**Importance:** %d  **Confidence:** %.2f\n", c.Importance, c.Confidence)

	if len(neighbors) == 0 {
		fmt.Fprintf(&b, "\n## Existing neighbors\n\nNone found.\n")
	} else {
		fmt.Fprintf(&b, "\n## Existing neighbors (%d)\n\n", len(neighbors))
		for i, n := range neighbors {
			fmt.Fprintf(&b, "### Neighbor %d (id: %s)\n", i+1, n.ID)
			fmt.Fprintf(&b, "**Kind:** %s  **Status:** %s\n", n.Kind, n.Status)
			fmt.Fprintf(&b, "**Content:** %s\n", n.Content)
			if n.Context != "" {
				fmt.Fprintf(&b, "**Context:** %s\n", n.Context)
			}
			fmt.Fprintf(&b, "**Confidence:** %.2f  **Importance:** %d\n\n", n.Confidence, n.Importance)
		}
	}

	fmt.Fprintf(&b, "\nDecide: should this candidate be added, updated, merged, superseded, discarded, or parked?")
	return b.String()
}
