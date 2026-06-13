package retrieval

import "unicode/utf8"

// ClampExcerpt returns content[s:e] with bounds clamped to [0, len(content)]
// and both endpoints aligned to UTF-8 rune boundaries, so the result is always
// valid UTF-8 even when a provenance span offset lands mid-rune (RFC P1; no
// mid-rune splits).
//
// This is the single canonical drill-down excerpt shaper shared by the HTTP
// handler (internal/api), the MCP handler (internal/mcpserver), and the
// embedded SDK (sdk/stowage) so the three surfaces cannot diverge (D-069,
// parity-lens BUG-5).
func ClampExcerpt(content string, s, e int) string {
	n := len(content)
	if s < 0 {
		s = 0
	}
	if s > n {
		s = n
	}
	if e < s {
		e = s
	}
	if e > n {
		e = n
	}
	if s == e {
		return ""
	}
	// Advance s past any UTF-8 continuation bytes to the next rune start.
	for s < n && !utf8.RuneStart(content[s]) {
		s++
	}
	if s > e {
		e = s
	}
	// Retract e backward to a rune start (e == n is always valid).
	for e > s && e < n && !utf8.RuneStart(content[e]) {
		e--
	}
	return content[s:e]
}
