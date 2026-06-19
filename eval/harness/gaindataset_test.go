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
	_ "github.com/hurtener/stowage/internal/gateway/bifrost"
	_ "github.com/hurtener/stowage/internal/gateway/openaicompat"
)

// TestGainDatasetMode runs the gain metric (memory-ON vs memory-OFF reader+judge,
// D-078) over a PUBLIC-BENCHMARK dataset rather than the hand-authored scenarios
// (D-096) — "make longmemeval_s / gain runnable". Each selected question's
// conversation is ingested + settled, then judged with and without retrieved
// memory; mean gain ≥ 0 is the RFC §12 release gate. Operator-triggered.
//
//	STOWAGE_EVAL_GATEWAY=bifrost STOWAGE_EVAL_GAIN=1 \
//	STOWAGE_EVAL_DATASET=longmemeval_s STOWAGE_EVAL_LIMIT=20 ... \
//	  go test -tags=fullmode -run TestGainDatasetMode -timeout 90m ./eval/harness/
func TestGainDatasetMode(t *testing.T) {
	if os.Getenv("STOWAGE_EVAL_GATEWAY") == "" || os.Getenv("STOWAGE_EVAL_GAIN") == "" {
		t.Skip("STOWAGE_EVAL_GATEWAY / STOWAGE_EVAL_GAIN not set — gain mode is operator-triggered")
	}
	datasetName := os.Getenv("STOWAGE_EVAL_DATASET")
	if datasetName == "" {
		datasetName = "longmemeval_s"
	}
	spec, err := datasets.MustLookup(datasetName)
	if err != nil {
		t.Fatalf("dataset select: %v", err)
	}
	limit := 20
	if v := os.Getenv("STOWAGE_EVAL_LIMIT"); v != "" {
		if n, perr := strconv.Atoi(v); perr == nil && n > 0 {
			limit = n
		}
	}
	settle := 10 * time.Minute
	if v := os.Getenv("STOWAGE_EVAL_SETTLE_TIMEOUT"); v != "" {
		if d, perr := time.ParseDuration(v); perr == nil && d > 0 {
			settle = d
		}
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
	convByID := map[string]datasets.Conversation{}
	for _, c := range convs {
		convByID[c.ID] = c
	}
	sort.Slice(questions, func(i, j int) bool { return questions[i].ID < questions[j].ID })
	if len(questions) > limit {
		questions = questions[:limit]
	}

	ctx := context.Background()
	var results []GainResult
	for _, q := range questions {
		conv, ok := convByID[q.ConvID]
		if !ok {
			t.Fatalf("question %s references unknown conversation %s", q.ID, q.ConvID)
		}
		// Fresh server per question so memories never cross-contaminate retrieval.
		srv := NewTestServer(t, "gain-ds-"+q.ID)
		if perr := srv.Gateway().Probe(ctx); perr != nil {
			t.Fatalf("gateway probe (check STOWAGE_EVAL_* / OPENROUTER_API_KEY): %v", perr)
		}
		runner := NewRunner(srv, RunConfig{EnableRerank: os.Getenv("STOWAGE_EVAL_RERANK_MODEL") != ""})
		gr, gerr := RunGainOverDataset(ctx, srv, runner, srv.Gateway(), conv, q, settle)
		if gerr != nil {
			t.Fatalf("gain over %s/%s: %v", datasetName, q.ID, gerr)
		}
		t.Logf("GAIN %s [%s]: off=%.2f(%s) on=%.2f(%s) gain=%+.2f",
			gr.ScenarioID, gr.Category, gr.QualityOff, gr.VerdictOff, gr.QualityOn, gr.VerdictOn, gr.Gain)
		results = append(results, gr)
	}

	summary := AggregateGain(results)
	outDir, _ := filepath.Abs("../../eval/results")
	_ = os.MkdirAll(outDir, 0o750)
	out := filepath.Join(outDir, fmt.Sprintf("gain-%s-n%d-%s.jsonl", datasetName, len(results), time.Now().UTC().Format("20060102T150405Z")))
	w, err := os.Create(out) //nolint:gosec
	if err != nil {
		t.Fatalf("create results: %v", err)
	}
	defer w.Close() //nolint:errcheck
	enc := json.NewEncoder(w)
	for _, r := range results {
		_ = enc.Encode(r)
	}
	_ = enc.Encode(map[string]any{"summary": summary, "dataset": datasetName})
	t.Logf("GAIN(%s) SUMMARY: mean_gain=%+.4f (on=%.4f off=%.4f) non_negative=%d/%d results=%s",
		datasetName, summary.MeanGain, summary.MeanQualityOn, summary.MeanQualityOff, summary.NonNegative, summary.Total, out)

	if summary.MeanGain < 0 {
		t.Errorf("RELEASE GATE: mean gain %.4f < 0 on %s — memory regressed task quality", summary.MeanGain, datasetName)
	}
}
