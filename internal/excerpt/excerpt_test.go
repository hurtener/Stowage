package excerpt

import "testing"

func TestClamp_ASCII(t *testing.T) {
	const s = "hello world"
	cases := []struct {
		name       string
		start, end int
		want       string
	}{
		{"full", 0, len(s), s},
		{"prefix", 0, 5, "hello"},
		{"negative start clamps to 0", -3, 4, "hell"},
		{"end past len clamps", 6, 999, "world"},
		{"start past len ⇒ empty", 99, 100, ""},
		{"end before start ⇒ empty", 5, 2, ""},
		{"equal ⇒ empty", 3, 3, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Clamp(s, c.start, c.end); got != c.want {
				t.Errorf("Clamp(%q, %d, %d) = %q, want %q", s, c.start, c.end, got, c.want)
			}
		})
	}
}

// TestClamp_RuneBoundaries: a span that lands mid-rune is aligned so the result is
// always valid UTF-8 (no mid-rune split).
func TestClamp_RuneBoundaries(t *testing.T) {
	const s = "café" // c=0, a=1, f=2, é=bytes 3-4 (len 5)
	// end=4 lands inside é → retract to byte 3.
	if got := Clamp(s, 0, 4); got != "caf" {
		t.Errorf("clamp end mid-rune = %q, want %q", got, "caf")
	}
	// start=4 lands inside é → advance to len → empty.
	if got := Clamp(s, 4, 5); got != "" {
		t.Errorf("clamp start mid-rune = %q, want empty", got)
	}
	// Whole string is preserved and valid.
	if got := Clamp(s, 0, len(s)); got != s {
		t.Errorf("clamp full = %q, want %q", got, s)
	}
}
