package reconcile_test

import (
	"encoding/json"
	"testing"

	"github.com/hurtener/stowage/internal/reconcile"
)

// FuzzParseDecision fuzzes the parseDecision path by feeding arbitrary JSON.
// The goal is to ensure no panic occurs for any input; invalid JSON or invalid
// action values should return an error, not panic.
func FuzzParseDecision(f *testing.F) {
	// Seed corpus: known-valid decisions.
	seeds := []string{
		`{"action":"add"}`,
		`{"action":"discard","reason":"duplicate"}`,
		`{"action":"update","target_ids":["id1"],"reason":"refined"}`,
		`{"action":"supersede","target_ids":["id2"]}`,
		`{"action":"merge","target_ids":["id1","id2"]}`,
		`{"action":"park","reason":"uncertain"}`,
		`{}`,
		`{"action":""}`,
		`{"action":"unknown"}`,
		`null`,
		`"string"`,
		`123`,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		// Must not panic regardless of input.
		d, err := reconcile.ExportParseDecision(json.RawMessage(data))
		if err != nil {
			return // errors are expected for invalid input
		}
		// If parse succeeded, the action must be a known value.
		switch d.Action {
		case "add", "update", "merge", "supersede", "discard", "park":
			// valid
		default:
			t.Errorf("parseDecision returned unknown action %q without error", d.Action)
		}
	})
}
