package sqlitestore

import (
	"strings"
	"unicode"
)

// ftsMatchArg turns arbitrary user query text into a safe FTS5 MATCH argument.
//
// Raw user text passed straight to FTS5 `MATCH` is interpreted as the FTS5 query
// grammar: bare operators (AND, OR, NOT, NEAR), column filters (`col:term`),
// prefix stars (`term*`), and unbalanced quotes (`"`) all either change the
// semantics or, more dangerously, raise a syntax error that aborts the whole
// lexical lane (parity-lens BUG-4). Postgres avoids this because the lane uses
// plainto_tsquery, which treats its input as plain text and ANDs the lexemes.
//
// We mirror that robustness profile: extract the alphanumeric terms from the
// input (dropping every operator/special character), wrap each as an FTS5 string
// literal, and AND them together (space = implicit AND in FTS5). Because only
// letters and digits survive tokenisation, no term can contain a double quote,
// so the produced expression is always syntactically valid.
//
// Returns "" when the input contains no indexable term; callers treat that as a
// clean empty result (no lane error), matching the empty-query early return.
func ftsMatchArg(query string) string {
	var terms []string
	var b strings.Builder
	flush := func() {
		if b.Len() > 0 {
			terms = append(terms, b.String())
			b.Reset()
		}
	}
	for _, r := range query {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			b.WriteRune(r)
		} else {
			flush()
		}
	}
	flush()

	if len(terms) == 0 {
		return ""
	}

	quoted := make([]string, len(terms))
	for i, t := range terms {
		// Safe: t contains only letters/digits, so no embedded '"' to escape.
		quoted[i] = `"` + t + `"`
	}
	return strings.Join(quoted, " ")
}
