// Package reconcile implements the Phase 08 reconciliation pipeline.
// It consumes CandidateBatch events from the extract stage and persists
// memories via the store.MemoryStore.Commit method (D-044, D-045).
//
// 8-step flow per candidate:
//  1. NormalizeContent + ContentHash
//  2. Exact-dedup check (GetByContentHash) → ActionDiscard if hit
//  3. Trust gate (confidence/importance threshold) → ActionPark if below floor
//  4. FindNeighbors (structural overlap)
//  5. Build LLM decision prompt
//  6. gateway.Complete → structured JSON decision
//  7. Validate and parse DecisionOutput
//  8. Build CommitSet + Commit
package reconcile

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"

	"github.com/hurtener/stowage/internal/store"
)

// wsRun matches one or more whitespace characters for collapse.
var wsRun = regexp.MustCompile(`\s+`)

// NormalizeContent trims leading/trailing whitespace and collapses internal
// whitespace runs to a single space. Case is preserved (D-045).
func NormalizeContent(s string) string {
	s = strings.TrimSpace(s)
	return wsRun.ReplaceAllString(s, " ")
}

// ContentHash returns the lowercase SHA-256 hex digest of the normalized content.
// An empty normalized string returns the SHA-256 of "".
func ContentHash(normalized string) string {
	h := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(h[:])
}

// bigramSet returns the set of character bigrams in s (lowercased for comparison).
func bigramSet(s string) map[string]struct{} {
	s = strings.ToLower(s)
	set := make(map[string]struct{}, max(len(s)-1, 0))
	for i := 0; i+1 < len(s); i++ {
		set[s[i:i+2]] = struct{}{}
	}
	return set
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// BigramJaccard returns the Jaccard similarity of the bigram sets of a and b.
// Returns 1.0 when both strings are empty (vacuously identical).
// Returns 0.0 when one string has no bigrams.
func BigramJaccard(a, b string) float64 {
	sa := bigramSet(a)
	sb := bigramSet(b)
	if len(sa) == 0 && len(sb) == 0 {
		return 1.0
	}
	if len(sa) == 0 || len(sb) == 0 {
		return 0.0
	}
	var inter int
	for k := range sa {
		if _, ok := sb[k]; ok {
			inter++
		}
	}
	union := len(sa) + len(sb) - inter
	if union == 0 {
		return 0.0
	}
	return float64(inter) / float64(union)
}

// MarshalPriorState serializes the mutable fields of a memory for D-017
// prior-state event payloads. Returns a compact JSON string.
func MarshalPriorState(m store.Memory) string {
	var b strings.Builder
	fmt.Fprintf(&b, `{"content":%s,"context":%s,"status":%s,"importance":%d,"confidence":%g,"stability":%g}`,
		jsonStr(m.Content), jsonStr(m.Context), jsonStr(m.Status),
		m.Importance, m.Confidence, m.Stability)
	return b.String()
}

// jsonStr returns a JSON-encoded string (minimal escaping).
func jsonStr(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}
