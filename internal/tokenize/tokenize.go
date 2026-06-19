// Package tokenize provides a dependency-free token-count ESTIMATE shared by the
// extraction transcript clamp, the playbook budget, and the day-one record
// TokenEstimate signal (A1 remediation). It is an estimate, not a tokenizer: it
// drives prompt clamping/budgeting, never correctness. A pure-Go BPE tokenizer was
// considered and rejected — it requires embedding a multi-MB vocab into the otherwise
// lean single static binary, a poor trade for a clamping estimate (D-091).
package tokenize

import "strings"

// Estimate approximates the BPE token count of s as the MAX of the two standard
// rules of thumb — ~4 bytes/token and ~0.75 words/token (OpenAI's "100 tokens
// ≈ 75 words"). max (not mean) is the right choice for a CLAMPING/BUDGET estimate: it
// never under-counts versus either rule, so the context is never silently overflowed.
// Whitespace-sparse text (code, blobs, CJK) tracks the byte rule; text with normal
// word spacing tracks the word rule — the bare len/4 under-counts the latter by ~25%.
//
// The byte rule uses len(s) (bytes), NOT rune count, deliberately: multibyte scripts
// (CJK) cost roughly one BPE token per CHARACTER, far more than bytes/4, so counting
// runes would under-count them — the dangerous direction for a clamp. Bytes/4 is the
// conservative shared heuristic the call sites used before this package (records.New,
// pipeline.roughTokens, playbook.estimateTokens). Always ≥1 for non-empty input.
func Estimate(s string) int {
	if s == "" {
		return 0
	}
	chars := len(s) // bytes, intentionally — see doc comment
	words := len(strings.Fields(s))

	byChars := chars / 4
	byWords := (words*4 + 2) / 3 // ceil(words / 0.75) == ceil(words * 4 / 3)

	est := byChars
	if byWords > est {
		est = byWords
	}
	if est < 1 {
		est = 1
	}
	return est
}
