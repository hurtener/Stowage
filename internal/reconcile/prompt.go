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

The candidate is the MOST RECENTLY asserted statement; the neighbors were recorded earlier.

Rules:
1. Prefer "add" only when no neighbor shares the same subject or claim.
2. When a neighbor shares the same subject but states a DIFFERENT value — a different number, quantity, duration, measurement, date, or status (e.g. "45 minutes each way" vs "about 30 minutes"; "9 months" vs "6 months"; "120 stars" vs "125 stars") — the candidate is the newer assertion: choose "supersede" to retire the older neighbor. Do NOT "add" a second memory for the same fact, and do NOT treat it as a duplicate.
3. Compare the underlying VALUE, not the surface wording or units: normalize quantities before judging contradiction ("$5,850" equals "5850"; "an hour and a half" equals "90 minutes"; "45 minutes each way" is more specific than, and contradicts, "about 30 minutes").
4. Prefer "update" when a neighbor covers the same subject with the SAME value but the candidate adds detail (no contradiction).
5. Prefer "merge" only when two or more neighbors are fragments of the same fact.
6. Use "discard" for greetings, filler, or exact re-statements with no new value.
7. Use "park" only when you have genuine uncertainty about correctness (not merely a value change — a clear value change is a supersede).
8. When an "Original conversation context" section is provided, use it to decide whether the candidate CORRECTS the neighbor's fact (same subject, updated value ⇒ supersede/update) or states a DIFFERENT fact that merely shares words or numbers (⇒ add). Two values that look contradictory in isolation ("30 minutes" vs "45 minutes each way") may be about DIFFERENT things in context — when the turns do not show them as the same fact, prefer "add" over "supersede".

Respond with a JSON object matching the schema. The "reason" field should be a concise one-sentence explanation.`
}

// ReconcileContext carries the raw conversation turns behind the candidate and its
// neighbors, so the decision can distinguish a correction from a distinct fact (D-108,
// Phase 29b). Zero value renders no context section (backward-compatible).
type ReconcileContext struct {
	CandidateTurns []store.Record            // raw turns the candidate was extracted from
	NeighborTurns  map[string][]store.Record // neighbor memory ID → its source turns
}

// renderTurns writes a compact "[role] content" list, trimming each turn's content.
func renderTurns(b *strings.Builder, turns []store.Record) {
	for _, t := range turns {
		c := strings.TrimSpace(t.Content)
		fmt.Fprintf(b, "  - [%s] %s\n", t.Role, c)
	}
}

// BuildUserPrompt returns the user-turn prompt for one candidate + its neighbors. When rc
// carries conversation turns, an "Original conversation context" section is appended so the
// model can judge correction-vs-distinct-fact from the source wording (D-108).
func BuildUserPrompt(c pipeline.Candidate, neighbors []store.Memory, rc ReconcileContext) string {
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
			fmt.Fprintf(&b, "**Confidence:** %.2f  **Importance:** %d\n", n.Confidence, n.Importance)
			fmt.Fprintf(&b, "**Trust:** use_count=%d  save_count=%d  trust_source=%s\n\n",
				n.UseCount, n.SaveCount, n.TrustSource)
		}
	}

	// Original conversation context (D-108): the raw turns behind the candidate and each
	// neighbor, so the model distinguishes a correction of the same fact from a different
	// fact that merely shares words.
	if len(rc.CandidateTurns) > 0 || len(rc.NeighborTurns) > 0 {
		fmt.Fprintf(&b, "\n## Original conversation context\n\n")
		if len(rc.CandidateTurns) > 0 {
			fmt.Fprintf(&b, "Turns the CANDIDATE was extracted from:\n")
			renderTurns(&b, rc.CandidateTurns)
		}
		for i, n := range neighbors {
			turns := rc.NeighborTurns[n.ID]
			if len(turns) == 0 {
				continue
			}
			fmt.Fprintf(&b, "Turns behind Neighbor %d (id: %s):\n", i+1, n.ID)
			renderTurns(&b, turns)
		}
	}

	fmt.Fprintf(&b, "\nDecide: should this candidate be added, updated, merged, superseded, discarded, or parked?")
	return b.String()
}
