package harness

import (
	"sort"
	"strings"
	"time"
)

// Scores holds the aggregate metrics for one CI eval run.
type Scores struct {
	AnswerContextHit float64 `json:"answer_context_hit"` // [0,1]
	HitCount         int     `json:"hit_count"`
	TotalQuestions   int     `json:"total_questions"`
	P50LatencyMs     float64 `json:"p50_latency_ms"`
	P95LatencyMs     float64 `json:"p95_latency_ms"`
}

// QuestionResult is the per-question result for one retrieve + score.
type QuestionResult struct {
	QuestionID string        `json:"question_id"`
	Query      string        `json:"query"`
	Expected   string        `json:"expected"`
	Hit        bool          `json:"hit"`
	Latency    time.Duration `json:"latency_ns"`
	Items      []string      `json:"items"` // content of retrieved items
}

// ComputeScores computes aggregate metrics from per-question results.
func ComputeScores(results []QuestionResult) Scores {
	if len(results) == 0 {
		return Scores{}
	}

	hits := 0
	latencies := make([]float64, 0, len(results))
	for _, r := range results {
		if r.Hit {
			hits++
		}
		latencies = append(latencies, float64(r.Latency.Milliseconds()))
	}

	sort.Float64s(latencies)
	p50 := percentile(latencies, 50)
	p95 := percentile(latencies, 95)

	return Scores{
		AnswerContextHit: float64(hits) / float64(len(results)),
		HitCount:         hits,
		TotalQuestions:   len(results),
		P50LatencyMs:     p50,
		P95LatencyMs:     p95,
	}
}

// AnswerContextHit returns true if expectedAnswer appears (case-insensitive)
// in any of the provided content strings.
//
// Short answers (< 4 characters after trimming, e.g. "2", "$12", "NYC") must
// match on token boundaries: a bare substring check lets "2" match inside
// "f/2.8 lens", which produced a spurious hit in the 2026-06-12 n=10 baseline.
// Longer answers keep the strict substring semantics (they are distinctive
// enough that boundary anchoring only adds false negatives on punctuation).
func AnswerContextHit(contents []string, expectedAnswer string) bool {
	lower := strings.ToLower(strings.TrimSpace(expectedAnswer))
	if lower == "" {
		return false
	}
	short := len([]rune(lower)) < 4
	for _, c := range contents {
		lc := strings.ToLower(c)
		if !short {
			if strings.Contains(lc, lower) {
				return true
			}
			continue
		}
		if containsToken(lc, lower) {
			return true
		}
	}
	return false
}

// containsToken reports whether needle occurs in haystack delimited by token
// boundaries on both sides. A boundary is a non-alphanumeric rune — EXCEPT
// joining punctuation (./-:,) sandwiched between alphanumerics, which keeps
// compound tokens like "f/2.8", "24-70mm", or "3:45" intact so a short answer
// such as "2" cannot match inside them.
func containsToken(haystack, needle string) bool {
	for start := 0; ; {
		i := strings.Index(haystack[start:], needle)
		if i < 0 {
			return false
		}
		i += start
		end := i + len(needle)
		if isBoundary(haystack, i-1, i-2) && isBoundary(haystack, end, end+1) {
			return true
		}
		start = i + 1
	}
}

// isBoundary reports whether the rune at index idx (with beyond one step
// further away from the match) terminates a token. Out-of-range counts as a
// boundary; joining punct with an alphanumeric beyond it does not.
func isBoundary(s string, idx, beyond int) bool {
	if idx < 0 || idx >= len(s) {
		return true
	}
	r := rune(s[idx])
	if isAlnum(r) {
		return false
	}
	if strings.ContainsRune("./-:,", r) && beyond >= 0 && beyond < len(s) && isAlnum(rune(s[beyond])) {
		return false // joining punct inside a compound token ("f/2.8", "24-70mm")
	}
	return true
}

func isAlnum(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
}

// percentile returns the p-th percentile value from a sorted slice.
func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(float64(len(sorted)-1) * p / 100.0)
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}
