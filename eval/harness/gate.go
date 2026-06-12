package harness

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// Baseline is the committed CI baseline for the gate check.
type Baseline struct {
	AnswerContextHit float64 `json:"answer_context_hit"`
	P50LatencyMs     float64 `json:"p50_latency_ms"`
	P95LatencyMs     float64 `json:"p95_latency_ms"`
}

// CheckGate compares actual scores against the baseline at baselinePath.
// Returns a non-nil error if any quality metric dropped. Latency clauses
// only apply when the baseline sets them > 0 — the CI baseline sets them to 0
// (disabled) because CI-runner latency is machine noise (observed: 79ms p50
// on a runner vs 25ms locally, tripping 3x headroom with identical quality);
// latency enforcement is the SLO rig's job on reference hardware (D-031).
func CheckGate(actual Scores, baselinePath string) error {
	data, err := os.ReadFile(baselinePath) //nolint:gosec // operator-supplied path
	if err != nil {
		return fmt.Errorf("eval gate: read baseline %s: %w", baselinePath, err)
	}
	var baseline Baseline
	if err := json.Unmarshal(data, &baseline); err != nil {
		return fmt.Errorf("eval gate: parse baseline: %w", err)
	}

	var failures []string

	if actual.AnswerContextHit < baseline.AnswerContextHit {
		failures = append(failures, fmt.Sprintf(
			"answer_context_hit %.4f < baseline %.4f (regression of %.4f)",
			actual.AnswerContextHit, baseline.AnswerContextHit,
			baseline.AnswerContextHit-actual.AnswerContextHit,
		))
	}
	// Latency: fail only if actual p50 exceeds 3x baseline (avoids noise on slow CI).
	if baseline.P50LatencyMs > 0 && actual.P50LatencyMs > baseline.P50LatencyMs*3 {
		failures = append(failures, fmt.Sprintf(
			"p50_latency_ms %.1f > 3× baseline %.1f",
			actual.P50LatencyMs, baseline.P50LatencyMs,
		))
	}
	if baseline.P95LatencyMs > 0 && actual.P95LatencyMs > baseline.P95LatencyMs*3 {
		failures = append(failures, fmt.Sprintf(
			"p95_latency_ms %.1f > 3× baseline %.1f",
			actual.P95LatencyMs, baseline.P95LatencyMs,
		))
	}

	if len(failures) > 0 {
		return fmt.Errorf("benchmark gate FAILED:\n  %s", strings.Join(failures, "\n  "))
	}
	return nil
}
