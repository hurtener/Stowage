//go:build fullmode

package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/hurtener/stowage/eval/datasets"
	_ "github.com/hurtener/stowage/internal/gateway/bifrost"      // full mode: bifrost runs the whole OpenRouter stack incl. rerank (D-075)
	_ "github.com/hurtener/stowage/internal/gateway/openaicompat" // still a valid driver (selectable via STOWAGE_EVAL_GATEWAY)
)

// TestFullMode runs a public-benchmark slice through the REAL pipeline (live
// gateway from STOWAGE_EVAL_* envs) and writes per-question results to
// eval/results/. Operator-triggered (never CI).
//
// The dataset is selected with STOWAGE_EVAL_DATASET (default "longmemeval"):
//
//	longmemeval     — oracle haystack (evidence sessions only)
//	longmemeval_s   — distractor haystack (~40–50 sessions/question) — the headline
//	locomo          — LoCoMo multi-session conversations
//
// All three resolve through the dataset registry (D-096) and run on the single
// harness.RunDataset path — the dataset is a parameter, not a forked runner.
//
// Rebased onto bifrost + the operator's cheaper models with rerank ENABLED (D-075):
//
//	STOWAGE_EVAL_GATEWAY=bifrost STOWAGE_EVAL_PROVIDER=openrouter \
//	STOWAGE_EVAL_BASE_URL=https://openrouter.ai/api \
//	STOWAGE_EVAL_RERANK_BASE_URL=https://openrouter.ai/api/v1 \
//	STOWAGE_EVAL_API_KEY_REF=env.OPENROUTER_API_KEY \
//	STOWAGE_EVAL_MODEL=inception/mercury-2 \
//	STOWAGE_EVAL_EMBED_MODEL=perplexity/pplx-embed-v1-0.6b STOWAGE_EVAL_EMBED_DIMS=1024 \
//	STOWAGE_EVAL_RERANK_MODEL=cohere/rerank-4-fast \
//	STOWAGE_EVAL_DATASET=longmemeval_s STOWAGE_EVAL_JUDGE=1 STOWAGE_EVAL_LIMIT=10 \
//	go test -tags=fullmode -run TestFullMode -timeout 90m ./eval/harness/
func TestFullMode(t *testing.T) {
	if os.Getenv("STOWAGE_EVAL_GATEWAY") == "" {
		t.Skip("STOWAGE_EVAL_GATEWAY not set — full mode is operator-triggered")
	}
	limit := 10
	if v := os.Getenv("STOWAGE_EVAL_LIMIT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}

	datasetName := os.Getenv("STOWAGE_EVAL_DATASET")
	if datasetName == "" {
		datasetName = "longmemeval"
	}
	spec, err := datasets.MustLookup(datasetName)
	if err != nil {
		t.Fatalf("dataset select: %v", err)
	}

	dataPath, _ := filepath.Abs(filepath.Join("../../eval/data", spec.DataFile))
	f, err := os.Open(dataPath) //nolint:gosec // operator-controlled path
	if err != nil {
		t.Fatalf("open dataset %q (run `stowage eval fetch --dataset %s` first): %v", datasetName, datasetName, err)
	}
	defer f.Close() //nolint:errcheck
	convs, questions, err := spec.Normalize(f)
	if err != nil {
		t.Fatalf("normalize %s: %v", datasetName, err)
	}

	srv := NewTestServer(t, "eval-full")
	// Rerank ENABLED in full mode (D-075): precise-profile retrieves run the
	// cross-encoder pass against the bifrost-wired rerank model. Disable with
	// STOWAGE_EVAL_RERANK_MODEL="".
	runner := NewRunner(srv, RunConfig{EnableRerank: os.Getenv("STOWAGE_EVAL_RERANK_MODEL") != ""})

	ctx := context.Background()

	// Fail fast on a bad/unreachable gateway (a bad key otherwise 401s every call
	// and the settle loop grinds until timeout — REPORT.md wart).
	if err := srv.Gateway().Probe(ctx); err != nil {
		t.Fatalf("gateway probe failed — check STOWAGE_EVAL_* / OPENROUTER_API_KEY: %v", err)
	}

	settle := 20 * time.Minute
	if v := os.Getenv("STOWAGE_EVAL_SETTLE_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			settle = d
		}
	}

	start := time.Now()
	res, err := RunDataset(ctx, srv, runner, convs, questions, RunDatasetOpts{
		Limit:         limit,
		Judge:         os.Getenv("STOWAGE_EVAL_JUDGE") != "", // Phase 20, D-076 — reader+judge
		Settle:        settle,
		PerConvSettle: 5 * time.Minute,
		EmbedSettle:   10 * time.Second, // async embed backfill before scoring
	})
	if err != nil {
		t.Fatalf("run dataset %s: %v", datasetName, err)
	}
	scores := res.Scores
	t.Logf("pipeline quiescent: %d active memories; judge_errors=%d", srv.ActiveMemoryCount(ctx), res.JudgeErrors)

	outDir, _ := filepath.Abs("../../eval/results")
	_ = os.MkdirAll(outDir, 0o750)
	out := filepath.Join(outDir, fmt.Sprintf("%s-n%d-%s.jsonl", datasetName, len(res.Results), time.Now().UTC().Format("20060102T150405Z")))
	w, err := os.Create(out) //nolint:gosec
	if err != nil {
		t.Fatalf("create results: %v", err)
	}
	defer w.Close() //nolint:errcheck
	enc := json.NewEncoder(w)
	for _, r := range res.Results {
		_ = enc.Encode(r)
	}
	summary := map[string]any{"summary": scores, "wall_time_sec": time.Since(start).Seconds(), "dataset": datasetName, "n": len(res.Results)}
	_ = enc.Encode(summary)

	quality := "n/a (judging off)"
	if scores.AnswerQuality != nil {
		quality = fmt.Sprintf("%.4f (%d judged)", *scores.AnswerQuality, scores.JudgedCount)
	}
	t.Logf("FULL-MODE dataset=%s n=%d answer_context_hit=%.4f (%d/%d) answer_quality=%s p50=%.0fms p95=%.0fms results=%s",
		datasetName, len(res.Results), scores.AnswerContextHit, scores.HitCount, scores.TotalQuestions, quality, scores.P50LatencyMs, scores.P95LatencyMs, out)
}
