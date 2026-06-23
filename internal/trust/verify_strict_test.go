package trust

import (
	"strings"
	"testing"
)

// Guards the schema against re-introducing OpenAI-strict-forbidden keywords (D-102):
// value/length constraints must live in server validation, not the schema.
func TestSchema_OpenAIStrict_NoForbiddenKeywords(t *testing.T) {
	s := string(verifySchema)
	for _, kw := range []string{"minimum", "maximum", "minItems", "maxItems", "minLength", "maxLength", "\"pattern\"", "\"format\"", "multipleOf"} {
		if strings.Contains(s, kw) {
			t.Errorf("schema uses OpenAI-strict-forbidden keyword %q — move the constraint to server validation (D-102)", kw)
		}
	}
}
