package tokenize

import "testing"

func TestEstimate(t *testing.T) {
	if Estimate("") != 0 {
		t.Error("empty = 0")
	}
	if Estimate("x") < 1 {
		t.Error("non-empty must be >= 1")
	}
	// A ~75-word English paragraph should land near ~100 tokens (OpenAI rule of thumb),
	// and well above the bare len/4 only for whitespace-sparse text. Monotonic + sane.
	short := Estimate("the quick brown fox jumps over the lazy dog")
	long := Estimate("the quick brown fox jumps over the lazy dog and then keeps running far away")
	if long <= short {
		t.Errorf("longer text must estimate more tokens: short=%d long=%d", short, long)
	}
	// 9 words / ~43 chars: byChars=10, byWords=ceil(9/0.75)=12 → max=12.
	if short < 9 || short > 14 {
		t.Errorf("9-word phrase estimate = %d, want ~12 (max heuristic)", short)
	}
	// The byte rule must NOT under-count multibyte text: a whitespace-free CJK string
	// is one strings.Fields word (byWords=2), so byChars must dominate using BYTES, not
	// runes. 10 CJK chars ≈ 30 bytes → 30/4 = 7. Rune-counting would give 10/4 = 2,
	// silently under-counting — the dangerous direction for a clamp (D-091).
	cjk := Estimate("私はメモリです年中無休") // 11 runes, 33 bytes
	if cjk < 8 {
		t.Errorf("CJK estimate must use bytes (>=8), got %d (rune-counting bug?)", cjk)
	}
}
