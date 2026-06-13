package harness_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/hurtener/stowage/eval/harness"
)

// ciFixturesDir returns the absolute path to eval/ci-fixtures/.
func ciFixturesDir(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	// filename: eval/harness/runner_test.go → dir: eval/harness/ → parent: eval/
	evalDir := filepath.Dir(filepath.Dir(filename))
	return filepath.Join(evalDir, "ci-fixtures")
}

// ciBaselinesPath returns the path to eval/baselines/ci.json.
func ciBaselinesPath(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	evalDir := filepath.Dir(filepath.Dir(filename))
	return filepath.Join(evalDir, "baselines", "ci.json")
}

// TestEvalCI runs the full CI eval in deterministic mock-gateway mode and
// verifies the gate passes against the committed baselines.
// This is the test exercised by `make eval-ci`.
//
// Not marked t.Parallel() because NewTestServer calls t.Setenv (env var
// isolation for the mock gateway script path).
func TestEvalCI(t *testing.T) {
	ctx := context.Background()

	srv := harness.NewTestServer(t, "eval-ci")
	runner := harness.NewRunner(srv, harness.RunConfig{
		FixturesDir: ciFixturesDir(t),
	})

	result, err := runner.RunCI(ctx)
	if err != nil {
		t.Fatalf("RunCI: %v", err)
	}

	t.Logf("CI eval scores: answer_context_hit=%.4f (%d/%d) p50=%.1fms p95=%.1fms",
		result.Scores.AnswerContextHit,
		result.Scores.HitCount,
		result.Scores.TotalQuestions,
		result.Scores.P50LatencyMs,
		result.Scores.P95LatencyMs,
	)
	// Check against committed baselines; skip gracefully if baseline absent.
	baselinePath := ciBaselinesPath(t)
	if _, err := os.Stat(baselinePath); os.IsNotExist(err) {
		t.Skipf("baseline not found at %s; run once to generate", baselinePath)
	}
	if err := harness.CheckGate(result.Scores, baselinePath); err != nil {
		t.Errorf("benchmark gate: %v", err)
	}
}

// TestEvalCIGateBites proves AC-3: disabling a load-bearing retrieval lane
// plants a regression that would fail the gate.
//
// It asserts that disabling EACH production lane individually
// — vector, queries, AND lexical — strictly lowers the score, using the lane
// FILTER alone (no limit cap). This is stronger than the prior single-vector
// check in two ways (D-067 Wave-A checkpoint):
//
//   - The lexical/queries lanes are now load-bearing in the fixture set, so a
//     regression in their class (BUG-4 was exactly this: the sqlite FTS5 MATCH
//     path that BOTH lanes share — see internal/store/sqlitestore/fts.go's
//     ftsMatchArg — hard-erroring on "?"-terminated queries and silently
//     dropping the lanes) is now caught. The "queries" lane exercises that
//     shared FTS-MATCH path; a dedicated keyword-phrased fixture (ci-q-lex-01,
//     answer "Kafka") makes the "lexical" LexicalSearch path load-bearing too —
//     LexicalSearch ANDs all query tokens, so natural-language questions
//     (full of stopwords) surface nothing, but a keyword query whose tokens all
//     appear in one memory does.
//   - The limit cap is DECOUPLED from the filter (CapLimitToOne is left false),
//     so the degradation is attributable to the missing lane, not to fetching
//     fewer results.
//
// Not marked t.Parallel() because NewTestServer calls t.Setenv (env var
// isolation for the mock gateway script path).
func TestEvalCIGateBites(t *testing.T) {
	ctx := context.Background()
	dir := ciFixturesDir(t)

	// Normal run (full limit, no lane disabled).
	srvN := harness.NewTestServer(t, "eval-gate-normal")
	normalResult, err := harness.NewRunner(srvN, harness.RunConfig{
		FixturesDir: dir,
	}).RunCI(ctx)
	if err != nil {
		t.Fatalf("normal RunCI: %v", err)
	}
	t.Logf("normal: answer_context_hit=%.4f (%d/%d)",
		normalResult.Scores.AnswerContextHit,
		normalResult.Scores.HitCount,
		normalResult.Scores.TotalQuestions)

	// Each production lane must be load-bearing: disabling it (filter only, no
	// limit cap) strictly lowers the score.
	for _, lane := range []string{"vector", "queries", "lexical"} {
		lane := lane
		t.Run(lane, func(t *testing.T) {
			//nolint:contextcheck // NewTestServer is test boot infra: it owns its
			// background goroutine lifecycle via t.Cleanup, not the test's ctx
			// (same as the top-level NewTestServer calls above).
			srv := harness.NewTestServer(t, "eval-gate-"+lane)
			degraded, err := harness.NewRunner(srv, harness.RunConfig{
				FixturesDir: dir,
				DisableLane: lane, // CapLimitToOne deliberately left false
			}).RunCI(ctx)
			if err != nil {
				t.Fatalf("degraded RunCI (lane=%s): %v", lane, err)
			}
			t.Logf("disable %-8s: answer_context_hit=%.4f (%d/%d)",
				lane,
				degraded.Scores.AnswerContextHit,
				degraded.Scores.HitCount,
				degraded.Scores.TotalQuestions)

			if degraded.Scores.AnswerContextHit >= normalResult.Scores.AnswerContextHit {
				t.Errorf("gate-bite FAILED for lane %q: the FILTER alone did not lower "+
					"scores (degraded=%.4f >= normal=%.4f) — lane is not load-bearing, "+
					"so a regression in it would slip past the benchmark gate",
					lane, degraded.Scores.AnswerContextHit, normalResult.Scores.AnswerContextHit)
			}
		})
	}
}
