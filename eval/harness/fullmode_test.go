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
	_ "github.com/hurtener/stowage/internal/gateway/openaicompat" // full mode uses the real HTTP driver
)

// TestFullMode runs a public-dataset slice through the REAL pipeline (live
// gateway from STOWAGE_EVAL_* envs) and writes per-question results to
// eval/results/. Operator-triggered: make eval-full (never CI).
//
//	STOWAGE_EVAL_GATEWAY=openaicompat STOWAGE_EVAL_BASE_URL=https://openrouter.ai/api/v1 \
//	STOWAGE_EVAL_API_KEY_REF=env.STOWAGE_TEST_OPENROUTER_KEY \
//	STOWAGE_EVAL_MODEL=google/gemini-3.5-flash \
//	STOWAGE_EVAL_EMBED_MODEL=google/gemini-embedding-2 STOWAGE_EVAL_EMBED_DIMS=3072 \
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
	runner := NewRunner(srv, RunConfig{})

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
	}
	// Real extraction is async: wait for the pipeline to settle.
	if err := srv.WaitForMemories(ctx, len(need)); err != nil {
		t.Logf("warning: %v (continuing — some conversations may have yielded no memories)", err)
	}
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
