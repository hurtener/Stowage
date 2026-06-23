package pipeline

import (
	"encoding/json"
	"testing"
)

// forbiddenStrictKeywords are JSON-Schema keywords OpenAI's strict structured-output
// mode rejects (and that constrained decoders don't enforce at generation anyway).
// Value/range constraints live in server-side validation instead (D-102).
var forbiddenStrictKeywords = []string{
	"minimum", "maximum", "minItems", "maxItems", "minLength", "maxLength",
	"pattern", "format", "multipleOf", "minProperties", "maxProperties",
}

// assertOpenAIStrict recursively verifies an object subschema is OpenAI-strict-compliant:
// no forbidden keywords anywhere, and every object has additionalProperties:false with
// every declared property present in `required` (D-102). Exported so other packages'
// schema tests can reuse it.
func assertOpenAIStrict(t *testing.T, raw json.RawMessage) {
	t.Helper()
	var node map[string]json.RawMessage
	if err := json.Unmarshal(raw, &node); err != nil {
		return // not an object node (e.g. a string/array leaf); nothing to check here
	}
	for _, kw := range forbiddenStrictKeywords {
		if _, bad := node[kw]; bad {
			t.Errorf("schema node uses OpenAI-strict-forbidden keyword %q (move the constraint to server validation, D-102)", kw)
		}
	}
	typeRaw, hasType := node["type"]
	var typ string
	_ = json.Unmarshal(typeRaw, &typ)
	if hasType && typ == "object" {
		if ap, ok := node["additionalProperties"]; !ok || string(ap) != "false" {
			t.Errorf("object node must set additionalProperties:false (got %q)", string(ap))
		}
		var props map[string]json.RawMessage
		_ = json.Unmarshal(node["properties"], &props)
		var req []string
		_ = json.Unmarshal(node["required"], &req)
		reqSet := map[string]bool{}
		for _, r := range req {
			reqSet[r] = true
		}
		for name, sub := range props {
			if !reqSet[name] {
				t.Errorf("property %q is not in `required` — OpenAI strict requires every property to be required (D-102)", name)
			}
			assertOpenAIStrict(t, sub) // recurse into property subschema
		}
	}
	// Recurse array item subschemas.
	if items, ok := node["items"]; ok {
		assertOpenAIStrict(t, items)
	}
}

// TestCandidateSchema_OpenAIStrict guards the extraction schema against regressions
// that would break OpenAI (and Azure) structured outputs (D-102).
func TestCandidateSchema_OpenAIStrict(t *testing.T) {
	assertOpenAIStrict(t, CandidateSchema)
}
