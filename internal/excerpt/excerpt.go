// Package excerpt is the leaf drill-down excerpt shaper (RFC P1). It lives in its own
// dependency-free package so consumers that must stay gateway-free — internal/traces
// (D-086), and the api/mcpserver/sdk drill-down surfaces — can share the ONE canonical
// shaper without transitively importing internal/retrieval (and thus internal/gateway).
package excerpt

import "unicode/utf8"

// Clamp returns content[s:e] with bounds clamped to [0, len(content)] and both
// endpoints aligned to UTF-8 rune boundaries, so the result is always valid UTF-8 even
// when a provenance span offset lands mid-rune (no mid-rune splits).
func Clamp(content string, s, e int) string {
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
