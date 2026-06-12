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
