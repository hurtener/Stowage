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
// The test runs the harness twice — normal and with a disabled lane — and
// asserts the degraded run scores strictly below the normal run. This is a
// test-only harness hook; no production code is modified.
//
// We disable the "vector" lane (a load-bearing lane every answer is surfaced by
// in this fixture set). Until h2 this test disabled "lexical", which only bit
// because the sqlite FTS lane hard-errored on the "?"-terminated fixture queries
// (BUG-4); with that fixed the lexical/queries lanes work and the answers are
// robustly multi-lane, so removing lexical alone no longer degrades (D-069).
//
// Not marked t.Parallel() because NewTestServer calls t.Setenv (env var
// isolation for the mock gateway script path).
func TestEvalCIGateBites(t *testing.T) {
	ctx := context.Background()
	dir := ciFixturesDir(t)

	// Normal run.
	srv1 := harness.NewTestServer(t, "eval-gate-normal")
	normalResult, err := harness.NewRunner(srv1, harness.RunConfig{
		FixturesDir: dir,
	}).RunCI(ctx)
	if err != nil {
		t.Fatalf("normal RunCI: %v", err)
	}

	// Degraded run: any item the "vector" lane contributed to is filtered out.
	srv2 := harness.NewTestServer(t, "eval-gate-degraded")
	degradedResult, err := harness.NewRunner(srv2, harness.RunConfig{
		FixturesDir: dir,
		DisableLane: "vector",
	}).RunCI(ctx)
	if err != nil {
		t.Fatalf("degraded RunCI: %v", err)
	}

	t.Logf("normal:   answer_context_hit=%.4f (%d/%d)",
		normalResult.Scores.AnswerContextHit,
		normalResult.Scores.HitCount,
		normalResult.Scores.TotalQuestions)
	t.Logf("degraded: answer_context_hit=%.4f (%d/%d)",
		degradedResult.Scores.AnswerContextHit,
		degradedResult.Scores.HitCount,
		degradedResult.Scores.TotalQuestions)

	// The gate MUST bite: degraded scores must be strictly lower.
	if degradedResult.Scores.AnswerContextHit >= normalResult.Scores.AnswerContextHit {
		t.Errorf("gate-bite test FAILED: disabling the vector lane did not lower scores "+
			"(degraded=%.4f >= normal=%.4f) — "+
			"check that the harness DisableLane hook is filtering lane results correctly",
			degradedResult.Scores.AnswerContextHit,
			normalResult.Scores.AnswerContextHit,
		)
	}
}
