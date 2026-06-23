package pipeline

import (
	"encoding/json"

	"github.com/hurtener/stowage/internal/identity"
)

// CandidateSchemaVersion is the version tag for the candidate JSON schema.
// Increment when the schema changes (Phase 08+ may add fields).
const CandidateSchemaVersion = "3"

// CandidateSchema is the JSON schema for the gateway-constrained extraction
// response (CLAUDE.md §10, D-040). It is sent as Schema in every
// gateway.CompleteRequest; the gateway seam validates the model response
// against it before returning.
//
// Kinds are restricted to the RFC §5.2 enum minus the reflection kinds
// (strategy, failure_mode), which arrive in Phase 19.
var CandidateSchema = json.RawMessage(`{
  "$schema": "http://json-schema.org/draft-07/schema#",
  "title": "ExtractionResponse",
  "description": "Stowage candidate-memory extraction response (schema v` + CandidateSchemaVersion + `)",
  "type": "object",
  "required": ["candidates"],
  "additionalProperties": false,
  "properties": {
    "candidates": {
      "type": "array",
      "items": {
        "type": "object",
        "required": [
          "kind",
          "content",
          "context",
          "entities",
          "keywords",
          "anticipated_queries",
          "topics",
          "importance",
          "confidence",
          "provenance"
        ],
        "additionalProperties": false,
        "properties": {
          "kind": {
            "type": "string",
            "enum": ["fact", "preference", "decision", "gotcha", "pattern", "task", "narrative"]
          },
          "content":  { "type": "string" },
          "context":  { "type": "string" },
          "entities": { "type": "array", "items": { "type": "string" } },
          "keywords": { "type": "array", "items": { "type": "string" } },
          "anticipated_queries": {
            "type": "array",
            "items": { "type": "string" }
          },
          "topics": {
            "type": "array",
            "items": { "type": "string" },
            "description": "keys of the provided topics this candidate pertains to (subset of the topic keys in the prompt); [] if none"
          },
          "importance": { "type": "integer", "description": "1 (trivial) to 5 (critical)" },
          "confidence": { "type": "number",  "description": "0.0 to 1.0" },
          "provenance": {
            "type": "array",
            "items": {
              "type": "object",
              "required": ["record_id", "span_start", "span_end"],
              "additionalProperties": false,
              "properties": {
                "record_id":  { "type": "string" },
                "span_start": { "type": "integer" },
                "span_end":   { "type": "integer" }
              }
            }
          }
        }
      }
    }
  }
}`)

// ValidKinds is the accepted candidate kind set for Phase 07 (RFC §5.2 subset).
// strategy and failure_mode are reserved for Phase 19 (reflection) — they are NOT
// in this map, so topic extraction can never emit them (and the reflection schema
// in internal/reflect can never emit topic kinds).
var ValidKinds = map[string]bool{
	"fact":       true,
	"preference": true,
	"decision":   true,
	"gotcha":     true,
	"pattern":    true,
	"task":       true,
	"narrative":  true,
}

// ReflectionKinds is the accepted candidate kind set for the Phase 19 reflection
// write-side (ACE §6a.2, D-077). Disjoint from ValidKinds.
var ReflectionKinds = map[string]bool{
	"strategy":     true,
	"failure_mode": true,
}

// IsReflectionKind reports whether kind is a reflection product (strategy /
// failure_mode). Used by reconcile to restrict reflection candidates' neighbor
// search to reflection kinds so a strategy cannot supersede a fact (D-077 #5).
func IsReflectionKind(kind string) bool { return ReflectionKinds[kind] }

// ReflectionKindList returns the reflection kinds as a slice (for NeighborQuery).
func ReflectionKindList() []string { return []string{"strategy", "failure_mode"} }

// ProvSpan links a candidate memory to a character range within a verbatim record.
type ProvSpan struct {
	RecordID  string `json:"record_id"`
	SpanStart int    `json:"span_start"`
	SpanEnd   int    `json:"span_end"`
}

// Candidate is an extracted not-yet-committed memory produced by the extract
// stage. Scope and BranchID are stamped on the containing CandidateBatch by the
// server (P3); the model never sets them.
//
// TrustSource and Stability are server-set provenance/seed fields (json:"-", never
// set by the model): the reflection constructor stamps them ("llm_reflected" + a
// seed stability) so reflection-origin memories are distinguishable and seeded;
// topic candidates leave them zero and inherit the "llm_extracted" / 1.0 defaults
// in reconcile (D-077 #4).
type Candidate struct {
	Kind               string     `json:"kind"`
	Content            string     `json:"content"`
	Context            string     `json:"context"`
	Entities           []string   `json:"entities"`
	Keywords           []string   `json:"keywords"`
	AnticipatedQueries []string   `json:"anticipated_queries"`
	Topics             []string   `json:"topics,omitempty"` // topic keys this candidate pertains to (D-089)
	Importance         int        `json:"importance"`
	Confidence         float64    `json:"confidence"`
	Provenance         []ProvSpan `json:"provenance"`
	TrustSource        string     `json:"-"` // server-set; "" → "llm_extracted"
	Stability          float64    `json:"-"` // server-set seed; 0 → 1.0
}

// CandidateList is the top-level object the model returns.
type CandidateList struct {
	Candidates []Candidate `json:"candidates"`
}

// CandidateBatch is emitted on the ExtractStage downstream channel. It is the
// typed unit Phase 08 (reconciliation) consumes.
//
// Scope and BranchID are stamped from the originating FlushedBuffer (P3): the
// model never controls which scope or branch a candidate belongs to.
type CandidateBatch struct {
	Scope      identity.Scope `json:"scope"`
	BufferKey  string         `json:"buffer_key"`
	BranchID   string         `json:"branch_id"`
	Candidates []Candidate    `json:"candidates"`
}
