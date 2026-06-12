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
func AnswerContextHit(contents []string, expectedAnswer string) bool {
	lower := strings.ToLower(expectedAnswer)
	for _, c := range contents {
		if strings.Contains(strings.ToLower(c), lower) {
			return true
		}
	}
	return false
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
