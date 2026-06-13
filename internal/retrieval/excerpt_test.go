package retrieval

// Tests for ClampExcerpt — the single canonical UTF-8-safe span extractor shared
// by the HTTP, MCP, and embedded SDK drill-down surfaces (RFC P1; D-069 / AC-4:
// verbatim spans, no mid-rune splits).

import (
	"testing"
	"unicode/utf8"
)

func TestClampExcerpt(t *testing.T) {
	t.Parallel()

	ascii := "Hello, World!"

	tests := []struct {
		name    string
		content string
		s, e    int
		want    string
	}{
		// Basic happy path.
		{"full", ascii, 0, len(ascii), ascii},
		{"mid", ascii, 7, 12, "World"},
		{"empty range", ascii, 5, 5, ""},
		// Bounds clamping.
		{"s negative", ascii, -3, 5, "Hello"},
		{"e > n", ascii, 7, 100, "World!"},
		{"s > n", ascii, 100, 200, ""},
		{"e < s", ascii, 7, 3, ""},
		// Multibyte UTF-8: "Héllo" (é = 0xC3 0xA9, 2 bytes)
		// byte layout: H(0) é-lead(1) é-cont(2) l(3) l(4) o(5)
		{"utf8 no split", "Héllo", 0, 6, "Héllo"},
		// s=1 is the lead byte of é (0xC3) → valid rune start, returns "éllo"
		{"utf8 at lead byte", "Héllo", 1, 6, "éllo"},
		// s=2 is the continuation byte of é (0xA9) → advance past it → s=3 → "llo"
		{"utf8 at continuation byte", "Héllo", 2, 6, "llo"},
		{"utf8 boundary end", "Héllo", 0, 2, "H"}, // e=2 (continuation of é) → retract to byte 1 → "H"
		// Chinese: 你好 (each char = 3 bytes)
		// byte layout: 你(0-2) 好(3-5)
		{"chinese full", "你好", 0, 6, "你好"},
		{"chinese first char", "你好", 0, 3, "你"},
		{"chinese mid split start", "你好", 1, 6, "好"}, // byte 1 is continuation → advance to byte 3
		{"chinese mid split end", "你好", 0, 4, "你"},   // byte 4 is continuation → retract to byte 3
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := ClampExcerpt(tc.content, tc.s, tc.e)
			if got != tc.want {
				t.Errorf("ClampExcerpt(%q, %d, %d) = %q want %q",
					tc.content, tc.s, tc.e, got, tc.want)
			}
			if !utf8.ValidString(got) {
				t.Errorf("ClampExcerpt(%q, %d, %d) = %q is not valid UTF-8",
					tc.content, tc.s, tc.e, got)
			}
		})
	}
}

// TestClampExcerptAlwaysValidUTF8 fuzzes mid-rune offsets over multibyte content
// and asserts the result is always valid UTF-8 (the BUG-5 invariant the embedded
// path previously violated by raw byte-slicing).
func TestClampExcerptAlwaysValidUTF8(t *testing.T) {
	t.Parallel()
	content := "café — 日本語 — naïve façade 🚀 done"
	n := len(content)
	for s := -2; s <= n+2; s++ {
		for e := -2; e <= n+2; e++ {
			got := ClampExcerpt(content, s, e)
			if !utf8.ValidString(got) {
				t.Fatalf("ClampExcerpt(content, %d, %d) = %q is not valid UTF-8", s, e, got)
			}
		}
	}
}
