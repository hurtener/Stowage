package reconcile

import (
	"encoding/json"
	"fmt"

	"github.com/hurtener/stowage/internal/store"
)

// DecisionSchema is the JSON schema the gateway uses to constrain the LLM's
// reconciliation decision response (D-040). The schema is sent as Schema in
// every CompleteRequest; the gateway seam validates the response against it.
var DecisionSchema = json.RawMessage(`{
  "$schema": "http://json-schema.org/draft-07/schema#",
  "title": "ReconcileDecision",
  "description": "Stowage reconciliation decision (Phase 08)",
  "type": "object",
  "required": ["action"],
  "additionalProperties": false,
  "properties": {
    "action": {
      "type": "string",
      "enum": ["add", "update", "merge", "supersede", "discard", "park"]
    },
    "content": {
      "type": "string",
      "description": "Synthesized content for update and merge actions (required for those actions)"
    },
    "target_ids": {
      "type": "array",
      "items": { "type": "string" },
      "description": "IDs of existing memories affected by update/merge/supersede; must be a subset of shown neighbors"
    },
    "links": {
      "type": "array",
      "description": "Typed directed edges to write from the new memory to existing neighbors",
      "items": {
        "type": "object",
        "required": ["target_id", "type"],
        "additionalProperties": false,
        "properties": {
          "target_id": { "type": "string" },
          "type": {
            "type": "string",
            "enum": ["supports", "contradicts"]
          }
        }
      }
    },
    "reason": {
      "type": "string",
      "description": "Human-readable explanation of the decision"
    }
  }
}`)

// DecisionLink is one typed directed edge declared in a reconciliation decision.
// The reconcile package converts these to store.Link rows with source='reconciler'.
type DecisionLink struct {
	TargetID string `json:"target_id"`
	Type     string `json:"type"` // "supports" | "contradicts"
}

// DecisionOutput is the structured output from the LLM reconciliation call.
type DecisionOutput struct {
	Action    string         `json:"action"`
	Content   string         `json:"content"` // M5: synthesized content for update/merge
	TargetIDs []string       `json:"target_ids"`
	Links     []DecisionLink `json:"links"`
	Reason    string         `json:"reason"`
}

// validateDecision checks that the action is a known ReconcileAction,
// that target IDs are present for actions that require them, and that
// content is non-empty for update and merge actions (M5). It also deduplicates
// target_ids in place, preserving first-occurrence order (m10).
func validateDecision(d *DecisionOutput) error {
	// Deduplicate target_ids preserving first-occurrence order (m10).
	if len(d.TargetIDs) > 1 {
		seen := make(map[string]bool, len(d.TargetIDs))
		deduped := make([]string, 0, len(d.TargetIDs))
		for _, id := range d.TargetIDs {
			if !seen[id] {
				seen[id] = true
				deduped = append(deduped, id)
			}
		}
		d.TargetIDs = deduped
	}

	switch store.ReconcileAction(d.Action) {
	case store.ActionAdd, store.ActionDiscard, store.ActionPark:
		// no target IDs or content required
	case store.ActionUpdate:
		if len(d.TargetIDs) == 0 {
			return fmt.Errorf("reconcile: decision action %q requires at least one target_id", d.Action)
		}
		if len(d.TargetIDs) != 1 {
			return fmt.Errorf("reconcile: decision action %q expects exactly one target_id, got %d", d.Action, len(d.TargetIDs))
		}
		if d.Content == "" {
			return fmt.Errorf("reconcile: decision action %q requires non-empty content", d.Action)
		}
	case store.ActionSupersede:
		if len(d.TargetIDs) == 0 {
			return fmt.Errorf("reconcile: decision action %q requires at least one target_id", d.Action)
		}
		if len(d.TargetIDs) != 1 {
			return fmt.Errorf("reconcile: decision action %q expects exactly one target_id, got %d", d.Action, len(d.TargetIDs))
		}
	case store.ActionMerge:
		if len(d.TargetIDs) < 2 {
			return fmt.Errorf("reconcile: decision action %q requires at least 2 target_ids, got %d", d.Action, len(d.TargetIDs))
		}
		if d.Content == "" {
			return fmt.Errorf("reconcile: decision action %q requires non-empty content", d.Action)
		}
	default:
		return fmt.Errorf("reconcile: unknown action %q in decision", d.Action)
	}
	return nil
}

// parseDecision unmarshals and validates the raw JSON from the gateway.
func parseDecision(raw json.RawMessage) (DecisionOutput, error) {
	var d DecisionOutput
	if err := json.Unmarshal(raw, &d); err != nil {
		return d, fmt.Errorf("reconcile: parse decision: %w", err)
	}
	if err := validateDecision(&d); err != nil {
		return d, err
	}
	return d, nil
}
