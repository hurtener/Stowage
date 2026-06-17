package harness

import "testing"

// TestAnswerContextHit_ShortAnswerTokenBoundary pins the 2026-06-12 baseline
// artifact: the expected answer "2" must NOT match inside "f/2.8" — short
// answers require token-boundary matches.
func TestAnswerContextHit_ShortAnswerTokenBoundary(t *testing.T) {
	cases := []struct {
		name     string
		contents []string
		expected string
		want     bool
	}{
		{"short digit inside spec string is not a hit", []string{"The user owns a Sony 24-70mm f/2.8 lens."}, "2", false},
		{"short digit as a real token is a hit", []string{"The user went to 2 appointments in March."}, "2", true},
		{"short answer with currency symbol matches on boundary", []string{"Each mug cost $12 for the coworkers."}, "$12", true},
		{"short answer at content start", []string{"2 appointments happened in March."}, "2", true},
		{"short answer at content end", []string{"The number of appointments was 2"}, "2", true},
		{"short alpha token must not match inside a word", []string{"The user visited NYCity hall."}, "NYC", false},
		{"long answers keep substring semantics", []string{"They waited over a year, sadly."}, "over a year", true},
		{"long answer absent", []string{"The user owns a camera."}, "over a year", false},
		{"empty expected never hits", []string{"anything"}, "", false},
		{"case-insensitive on both sides", []string{"Each mug was $12."}, "$12", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := AnswerContextHit(tc.contents, tc.expected); got != tc.want {
				t.Errorf("AnswerContextHit(%q, %q) = %v, want %v", tc.contents, tc.expected, got, tc.want)
			}
		})
	}
}

// TestAnswerContextHit_Normalization covers the Phase 20 deterministic layers:
// number-word equivalence (both directions) and either-direction phrase match.
// Crucially it re-pins that the new phrase layer does NOT reopen the f/2.8
// artifact (single-number golds stay on the token-boundary path).
func TestAnswerContextHit_Normalization(t *testing.T) {
	cases := []struct {
		name     string
		contents []string
		expected string
		want     bool
	}{
		// Number-word equivalence (form mismatch the retriever can't fix).
		{"word gold matches digit content", []string{"User completed 5 painting classes."}, "five", true},
		{"digit gold matches word content", []string{"User completed five painting classes."}, "5", true},
		{"ordinal word gold matches digit", []string{"It was their 2 appointment that month."}, "second", true},
		{"number word absent is a miss", []string{"User completed three classes."}, "five", false},
		// Either-direction phrase match (point-of-view / article noise).
		{"my vs the bed", []string{"User keeps old sneakers under the bed."}, "under my bed", true},
		{"the vs my bed", []string{"User keeps sneakers under my bed."}, "under the bed", true},
		{"phrase absent is a miss", []string{"User stores sneakers in the closet."}, "under my bed", false},
		// Guards: reasoning gaps are NOT credited (that is the judge's job).
		{"sum is not credited", []string{"User added 17 postcards, then 8 more."}, "25", false},
		// Guard: the phrase layer must not reopen the f/2.8 artifact.
		{"single number still not inside f/2.8", []string{"Sony 24-70mm f/2.8 lens."}, "2", false},
		{"single number word not loosely matched", []string{"Sony 24-70mm f/2.8 lens."}, "two", false},
		// Guard: number-word variants are TOKENS — no substring matching inside
		// larger words or compound numbers (the adversarial-review blocker).
		{"digit gold not inside the word weight", []string{"User wants to lose weight this year."}, "8", false},
		{"digit gold not inside compound twenty-five", []string{"User owns twenty-five books."}, "5", false},
		{"digit gold not inside eighty", []string{"The user turned eighty last month."}, "8", false},
		{"number-word gold not inside eighteen", []string{"User has eighteen cousins."}, "eight", false},
		{"number-word gold matches on a real boundary", []string{"User owns eight cats."}, "eight", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := AnswerContextHit(tc.contents, tc.expected); got != tc.want {
				t.Errorf("AnswerContextHit(%q, %q) = %v, want %v", tc.contents, tc.expected, got, tc.want)
			}
		})
	}
}
