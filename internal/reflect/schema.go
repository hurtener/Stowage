package reflect

import "encoding/json"

// ReflectionSchemaVersion tags the reflection response JSON schema.
const ReflectionSchemaVersion = "1"

// reflectionSchema is the JSON schema the gateway constrains the reflection
// response to (CLAUDE.md §10 / D-040 — schema-constrained, no free-text JSON
// parsing). It mirrors the topic-extraction candidate shape but its kind enum is
// the REFLECTION kinds only (strategy, failure_mode); the topic schema's enum
// excludes them, so the two paths can never cross-emit (D-077 #3).
var reflectionSchema = json.RawMessage(`{
  "$schema": "http://json-schema.org/draft-07/schema#",
  "title": "ReflectionResponse",
  "description": "Stowage reflection response (schema v` + ReflectionSchemaVersion + `)",
  "type": "object",
  "required": ["reflections"],
  "additionalProperties": false,
  "properties": {
    "reflections": {
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
            "enum": ["strategy", "failure_mode"]
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
