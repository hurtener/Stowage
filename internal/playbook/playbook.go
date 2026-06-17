// Package playbook assembles a deterministic, sectioned, utility-ranked,
// budget-packed view over a scope's strategy / failure_mode / building-block
// memories (RFC §6a.3, D-072).
//
// # LLM-free (CLAUDE.md §6, P5)
//
// This package NEVER calls the gateway and never imports any
// internal/gateway* package. Playbook evolution happens only through delta
// reconciliation (which produces and updates the underlying memories); the
// playbook itself is a pure, deterministic projection over those memories plus
// the pure internal/scoring functions. The no-gateway-import invariant is
// enforced by TestPlaybookNoGatewayImport (AC-1) — the §6 lint for this package.
//
// # Determinism + append-bias (ACE, brief 05)
//
// Assemble is a pure function of the stored memories and their utility counters:
// the same scope + same memories yield a byte-identical playbook every run
// (golden-tested). Items within a section are ordered by utility score
// descending with a stable ULID tiebreak, so re-fetching after a new,
// lower-ranked memory is added keeps the existing prefix stable (the KV-cache
// warmth property — append-biased ordering). Monolithic LLM rewrites would
// collapse that context; here there is no rewrite, only re-projection.
package playbook

import "github.com/hurtener/stowage/internal/store"

// Options tunes a single Assemble call.
//
// Topic restriction is intentionally NOT an option: memories carry no
// topic-key column in the v1 schema (RFC §8.1), so a topic filter would be a
// dead knob today; sections are grouped by kind. This is the one documented
// deviation from the phase-h5 design block (recorded in the plan + D-072).
type Options struct {
	// SessionID, when non-empty, narrows assembly to a single session by
	// appending it to the assembly scope (session-affinity). Empty = the whole
	// scope passed to Assemble.
	SessionID string

	// TokenBudget caps the estimated tokens of packed item content. A value
	// <= 0 falls back to DefaultTokenBudget. Surfaces resolve this from the
	// active profile (config.PlaybookBudgetForProfile, D-072/D-042).
	TokenBudget int
}

// DefaultTokenBudget is the fallback budget used when Options.TokenBudget <= 0.
// It mirrors the "assistant" profile budget so a zero-config direct caller
// (and tests) still pack a useful playbook.
const DefaultTokenBudget = 2000

// ProvenanceRef is a compact verbatim-span reference attached to a packed item
// for P1 drill-down. It mirrors store.Provenance's identifying fields.
type ProvenanceRef struct {
	RecordID  string
	SpanStart int
	SpanEnd   int
}

// Item is one ranked memory in a section.
type Item struct {
	MemoryID   string
	Kind       string
	Content    string
	Score      float64
	Provenance []ProvenanceRef
}

// Section groups the packed items of a single kind, in playbook-kind order.
type Section struct {
	Title string
	Kind  string
	Items []Item
}

// BudgetInfo reports how the budget was spent, for observability and tests.
type BudgetInfo struct {
	TokenBudget int // the effective budget used (after the DefaultTokenBudget fallback)
	TokensUsed  int // estimated tokens of the packed item content
	ItemsTotal  int // candidate items considered (all matching kinds in scope)
	ItemsPacked int // items that fit the budget
}

// Playbook is the assembled, sectioned, budget-packed result.
type Playbook struct {
	Sections []Section
	Budget   BudgetInfo
}

// playbookKinds is the ordered set of memory kinds the playbook assembles, in
// section order: the §6a reflection products (strategy, failure_mode) first,
// then the building-block kinds (decision, gotcha, pattern). Ordering here is
// the section ordering in the output.
var playbookKinds = []string{"strategy", "failure_mode", "decision", "gotcha", "pattern"}

// kindTitles maps each playbook kind to its human-readable section title.
var kindTitles = map[string]string{
	"strategy":     "Strategies",
	"failure_mode": "Failure modes",
	"decision":     "Decisions",
	"gotcha":       "Gotchas",
	"pattern":      "Patterns",
}

// Kinds returns a copy of the ordered playbook kinds. Exposed so surfaces and
// tests reference the single source of truth rather than re-listing kinds.
func Kinds() []string {
	out := make([]string, len(playbookKinds))
	copy(out, playbookKinds)
	return out
}

// estimateTokens approximates the token count of s using the 4-chars ≈ 1 token
// heuristic shared with records.New / pipeline.roughTokens (D-024). Non-empty
// content always costs at least one token so a tiny memory is never "free".
func estimateTokens(s string) int {
	n := len(s) / 4
	if n < 1 && len(s) > 0 {
		return 1
	}
	return n
}

// toProvRefs maps store provenance rows to compact refs (used by assemble.go).
func toProvRefs(rows []store.Provenance) []ProvenanceRef {
	if len(rows) == 0 {
		return nil
	}
	out := make([]ProvenanceRef, len(rows))
	for i, p := range rows {
		out[i] = ProvenanceRef{RecordID: p.RecordID, SpanStart: p.SpanStart, SpanEnd: p.SpanEnd}
	}
	return out
}
