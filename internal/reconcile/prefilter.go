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
	"encoding/json"
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

// priorStateJSON is the canonical serialization format for D-017 prior-state
// event payloads. Phase 15 rollback will consume this verbatim.
type priorStateJSON struct {
	ID          string        `json:"id"`
	Kind        string        `json:"kind"`
	Content     string        `json:"content"`
	Context     string        `json:"context,omitempty"`
	Status      string        `json:"status"`
	Importance  int           `json:"importance"`
	Confidence  float64       `json:"confidence"`
	TrustSource string        `json:"trust_source"`
	MatchCount  int64         `json:"match_count"`
	InjectCount int64         `json:"inject_count"`
	UseCount    int64         `json:"use_count"`
	SaveCount   int64         `json:"save_count"`
	FailCount   int64         `json:"fail_count,omitempty"`
	NoiseCount  int64         `json:"noise_count,omitempty"`
	Stability   float64       `json:"stability"`
	ValidFrom   int64         `json:"valid_from,omitempty"`
	ValidUntil  int64         `json:"valid_until,omitempty"`
	EpisodeID   string        `json:"episode_id,omitempty"`
	PrivacyZone string        `json:"privacy_zone,omitempty"`
	ContentHash string        `json:"content_hash,omitempty"`
	CreatedAt   int64         `json:"created_at"`
	UpdatedAt   int64         `json:"updated_at"`
	Entities    []string      `json:"entities,omitempty"`
	Keywords    []string      `json:"keywords,omitempty"`
	Queries     []string      `json:"queries,omitempty"`
	Provenance  []provRefJSON `json:"provenance,omitempty"`
}

// provRefJSON is a compact provenance reference for prior-state payloads.
type provRefJSON struct {
	RecordID  string `json:"record_id"`
	SpanStart int    `json:"span_start,omitempty"`
	SpanEnd   int    `json:"span_end,omitempty"`
}

// MarshalPriorState serializes the full prior state of a memory (all scalar
// fields + junction rows) for D-017 prior-state event payloads.
// Phase 15 rollback will consume this payload verbatim.
func MarshalPriorState(m store.Memory, j store.MemoryJunctions) string {
	p := priorStateJSON{
		ID:          m.ID,
		Kind:        m.Kind,
		Content:     m.Content,
		Context:     m.Context,
		Status:      m.Status,
		Importance:  m.Importance,
		Confidence:  m.Confidence,
		TrustSource: m.TrustSource,
		MatchCount:  m.MatchCount,
		InjectCount: m.InjectCount,
		UseCount:    m.UseCount,
		SaveCount:   m.SaveCount,
		FailCount:   m.FailCount,
		NoiseCount:  m.NoiseCount,
		Stability:   m.Stability,
		ValidFrom:   m.ValidFrom,
		ValidUntil:  m.ValidUntil,
		EpisodeID:   m.EpisodeID,
		PrivacyZone: m.PrivacyZone,
		ContentHash: m.ContentHash,
		CreatedAt:   m.CreatedAt,
		UpdatedAt:   m.UpdatedAt,
		Entities:    j.Entities,
		Keywords:    j.Keywords,
		Queries:     j.Queries,
	}
	for _, prov := range j.Provenance {
		p.Provenance = append(p.Provenance, provRefJSON{
			RecordID:  prov.RecordID,
			SpanStart: prov.SpanStart,
			SpanEnd:   prov.SpanEnd,
		})
	}
	b, err := json.Marshal(p)
	if err != nil {
		// Fallback: should not occur since priorStateJSON contains only
		// JSON-safe types. Return minimal payload so the event is not lost.
		return `{"content":"[serialization error]"}`
	}
	return string(b)
}
