package pipeline

import (
	"encoding/json"

	"github.com/hurtener/stowage/internal/identity"
)

// CandidateSchemaVersion is the version tag for the candidate JSON schema.
// Increment when the schema changes (Phase 08+ may add fields).
const CandidateSchemaVersion = "1"

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
          "content":  { "type": "string", "minLength": 1 },
          "context":  { "type": "string" },
          "entities": { "type": "array", "items": { "type": "string" } },
          "keywords": { "type": "array", "items": { "type": "string" } },
          "anticipated_queries": {
            "type": "array",
            "items": { "type": "string" }
          },
          "importance": { "type": "integer", "minimum": 1, "maximum": 5 },
          "confidence": { "type": "number",  "minimum": 0, "maximum": 1 },
          "provenance": {
            "type": "array",
            "minItems": 1,
            "items": {
              "type": "object",
              "required": ["record_id", "span_start", "span_end"],
              "additionalProperties": false,
              "properties": {
                "record_id":  { "type": "string" },
                "span_start": { "type": "integer", "minimum": 0 },
                "span_end":   { "type": "integer", "minimum": 0 }
              }
            }
          }
        }
      }
    }
  }
}`)

// ValidKinds is the accepted candidate kind set for Phase 07 (RFC §5.2 subset).
// strategy and failure_mode are reserved for Phase 19 (reflection).
var ValidKinds = map[string]bool{
	"fact":       true,
	"preference": true,
	"decision":   true,
	"gotcha":     true,
	"pattern":    true,
	"task":       true,
	"narrative":  true,
}

// ProvSpan links a candidate memory to a character range within a verbatim record.
type ProvSpan struct {
	RecordID  string `json:"record_id"`
	SpanStart int    `json:"span_start"`
	SpanEnd   int    `json:"span_end"`
}

// Candidate is an extracted not-yet-committed memory produced by the extract
// stage. Scope and BranchID are stamped on the containing CandidateBatch by the
// server (P3); the model never sets them.
type Candidate struct {
	Kind               string     `json:"kind"`
	Content            string     `json:"content"`
	Context            string     `json:"context"`
	Entities           []string   `json:"entities"`
	Keywords           []string   `json:"keywords"`
	AnticipatedQueries []string   `json:"anticipated_queries"`
	Importance         int        `json:"importance"`
	Confidence         float64    `json:"confidence"`
	Provenance         []ProvSpan `json:"provenance"`
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
