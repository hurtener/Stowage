package retrieval

import (
	"strings"
	"unicode"
)

// stopWords is a minimal English stop-word set for the structured lane.
// The structured lane uses entity/keyword overlap (FindNeighbors), not FTS,
// so this list is intentionally conservative.
var stopWords = map[string]bool{
	"a": true, "an": true, "the": true,
	"is": true, "are": true, "was": true, "were": true,
	"be": true, "been": true, "being": true,
	"have": true, "has": true, "had": true,
	"do": true, "does": true, "did": true,
	"will": true, "would": true, "could": true, "should": true,
	"may": true, "might": true, "can": true,
	"in": true, "on": true, "at": true, "by": true,
	"for": true, "with": true, "to": true, "from": true,
	"of": true, "and": true, "or": true, "not": true,
	"it": true, "its": true, "this": true, "that": true,
	"what": true, "how": true, "why": true, "when": true,
	"where": true, "which": true, "who": true,
	"i": true, "me": true, "my": true, "we": true, "our": true,
	"you": true, "your": true, "he": true, "she": true, "they": true,
	"him": true, "her": true, "them": true, "his": true, "their": true,
	"use": true, "used": true, "using": true,
	"get": true, "got": true, "make": true, "made": true,
}

// Tokenize extracts significant words from a query for the structured lane.
// Words shorter than 2 chars and stop words are excluded.
func Tokenize(query string) []string {
	query = strings.ToLower(strings.TrimSpace(query))
	words := strings.FieldsFunc(query, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	seen := make(map[string]bool, len(words))
	out := make([]string, 0, len(words))
	for _, w := range words {
		if len(w) < 2 || stopWords[w] || seen[w] {
			continue
		}
		seen[w] = true
		out = append(out, w)
	}
	return out
}
