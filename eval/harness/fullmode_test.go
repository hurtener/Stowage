//go:build fullmode

package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"testing"
	"time"

	"github.com/hurtener/stowage/eval/datasets"
	"github.com/hurtener/stowage/eval/datasets/longmemeval"
	_ "github.com/hurtener/stowage/internal/gateway/bifrost"      // full mode: bifrost runs the whole OpenRouter stack incl. rerank (D-075)
	_ "github.com/hurtener/stowage/internal/gateway/openaicompat" // still a valid driver (selectable via STOWAGE_EVAL_GATEWAY)
)

// TestFullMode runs a public-dataset slice through the REAL pipeline (live
// gateway from STOWAGE_EVAL_* envs) and writes per-question results to
// eval/results/. Operator-triggered: make eval-full (never CI).
//
// Rebased onto bifrost + the operator's cheaper models with rerank ENABLED
// (D-075): bifrost auto-wires a Cohere-shape custom provider so a single gateway
// runs embed + complete + rerank on OpenRouter with one key.
//
//	STOWAGE_EVAL_GATEWAY=bifrost STOWAGE_EVAL_PROVIDER=openrouter \
//	STOWAGE_EVAL_BASE_URL=https://openrouter.ai/api/v1 \
//	STOWAGE_EVAL_API_KEY_REF=env.OPENROUTER_API_KEY \
//	STOWAGE_EVAL_MODEL=inception/mercury-2 \
//	STOWAGE_EVAL_EMBED_MODEL=perplexity/pplx-embed-v1-0.6b STOWAGE_EVAL_EMBED_DIMS=1024 \
//	STOWAGE_EVAL_RERANK_MODEL=cohere/rerank-4-fast \
//	STOWAGE_EVAL_LIMIT=10 go test -tags=fullmode -run TestFullMode -timeout 90m ./eval/harness/
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

	dataPath, _ := filepath.Abs("../../eval/data/longmemeval/longmemeval.json")
	f, err := os.Open(dataPath)
	if err != nil {
		t.Fatalf("open dataset (run `stowage eval fetch --dataset longmemeval` first): %v", err)
	}
	defer f.Close() //nolint:errcheck
	convs, questions, err := longmemeval.Normalize(f)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}

	// Deterministic question slice; ingest only the conversations they need.
	sort.Slice(questions, func(i, j int) bool { return questions[i].ID < questions[j].ID })
	if len(questions) > limit {
		questions = questions[:limit]
	}
	need := map[string]bool{}
	for _, q := range questions {
		need[q.ConvID] = true
	}
	convByID := map[string]datasets.Conversation{}
	for _, c := range convs {
		convByID[c.ID] = c
	}

	srv := NewTestServer(t, "eval-full")
	// Rerank ENABLED in full mode (D-075): the runner issues precise-profile
	// retrieves so the cross-encoder pass runs against the bifrost-wired rerank
	// model. Disable via STOWAGE_EVAL_RERANK_MODEL="" (then the harness skips the
	// WithRerankModel wiring and precise just runs without a rerank model).
	runner := NewRunner(srv, RunConfig{EnableRerank: os.Getenv("STOWAGE_EVAL_RERANK_MODEL") != ""})

	ctx := context.Background()
	start := time.Now()
	for id := range need {
		c, ok := convByID[id]
		if !ok {
			t.Fatalf("question references unknown conversation %s", id)
		}
		fix := toFixture(c)
		if _, err := ingestFlushFull(ctx, runner, &fix); err != nil {
			t.Fatalf("ingest %s: %v", id, err)
		}
		// Pace ingestion: settle each conversation before the next so the
		// bounded pipeline channel never sees a burst (real deployments see
		// conversations over time, not the whole haystack in one second).
		if err := srv.WaitForQuiescence(ctx, 5*time.Minute); err != nil {
			t.Logf("conversation %s slow to settle (re-enqueue sweep will recover): %v", id, err)
		}
	}
	// Real extraction is async: hard settle barrier — scoring against a
	// partially-ingested store invalidates the run (2026-06-12 lesson).
	settle := 20 * time.Minute
	if v := os.Getenv("STOWAGE_EVAL_SETTLE_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			settle = d
		}
	}
	if err := srv.WaitForQuiescence(ctx, settle); err != nil {
		t.Fatalf("pipeline did not settle — run would be invalid: %v", err)
	}
	t.Logf("pipeline quiescent: %d active memories from %d conversations", srv.ActiveMemoryCount(ctx), len(need))
	time.Sleep(10 * time.Second) // embed backfill settle

	results := make([]QuestionResult, 0, len(questions))
	for _, q := range questions {
		qr, err := runner.scoreQuestion(ctx, QuestionFixture{
			ID: q.ID, Text: q.Text, ConvID: q.ConvID, Category: q.Category,
			Expected: struct {
				Answer string `json:"answer"`
			}{Answer: q.Expected.Answer},
		})
		if err != nil {
			t.Fatalf("score %s: %v", q.ID, err)
		}
		results = append(results, qr)
	}
	scores := ComputeScores(results)

	outDir, _ := filepath.Abs("../../eval/results")
	_ = os.MkdirAll(outDir, 0o750)
	out := filepath.Join(outDir, fmt.Sprintf("longmemeval-n%d-%s.jsonl", len(questions), time.Now().UTC().Format("20060102T150405Z")))
	w, err := os.Create(out) //nolint:gosec
	if err != nil {
		t.Fatalf("create results: %v", err)
	}
	defer w.Close() //nolint:errcheck
	enc := json.NewEncoder(w)
	for _, r := range results {
		_ = enc.Encode(r)
	}
	_ = enc.Encode(map[string]any{"summary": scores, "wall_time_sec": time.Since(start).Seconds(), "dataset": "longmemeval_s_cleaned", "n": len(questions)})
	t.Logf("FULL-MODE n=%d answer_context_hit=%.4f (%d/%d) p50=%.0fms p95=%.0fms results=%s",
		len(questions), scores.AnswerContextHit, scores.HitCount, scores.TotalQuestions, scores.P50LatencyMs, scores.P95LatencyMs, out)
}

func toFixture(c datasets.Conversation) ConvFixture {
	fix := ConvFixture{ID: c.ID}
	for _, s := range c.Sessions {
		sf := SessionFixture{ID: s.ID}
		for _, turn := range s.Turns {
			sf.Turns = append(sf.Turns, TurnFixture{Role: turn.Role, Content: turn.Content})
		}
		fix.Sessions = append(fix.Sessions, sf)
	}
	return fix
}

// ingestFlushFull ingests a conversation and explicitly flushes its buffer
// (full mode: no mock scripts — the real gateway extracts).
func ingestFlushFull(ctx context.Context, r *Runner, fix *ConvFixture) ([]string, error) {
	ids, err := r.ingestConversation(ctx, fix)
	if err != nil {
		return nil, err
	}
	if err := r.flushBuffer(ctx, fix.ID); err != nil {
		return nil, err
	}
	return ids, nil
}
