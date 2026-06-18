//go:build fullmode

package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/hurtener/stowage/eval/gain"
	_ "github.com/hurtener/stowage/internal/gateway/bifrost"
	_ "github.com/hurtener/stowage/internal/gateway/openaicompat"
)

// TestGainMode runs the gain harness (Phase 20b, D-078) over the committed gain
// scenarios with the live gateway: each scenario is answered by the reader+judge
// with retrieved memory context (memory-ON) and without (memory-OFF); gain =
// quality(on) − quality(off). The aggregate mean-gain ≥ 0 is the RFC §12 release
// gate. Operator-triggered (needs STOWAGE_EVAL_GATEWAY + STOWAGE_EVAL_GAIN); never
// CI. See fullmode_test.go for the gateway env config.
//
//	STOWAGE_EVAL_GATEWAY=bifrost STOWAGE_EVAL_GAIN=1 ... \
//	  go test -tags=fullmode -run TestGainMode -timeout 60m ./eval/harness/
func TestGainMode(t *testing.T) {
	if os.Getenv("STOWAGE_EVAL_GATEWAY") == "" || os.Getenv("STOWAGE_EVAL_GAIN") == "" {
		t.Skip("STOWAGE_EVAL_GATEWAY / STOWAGE_EVAL_GAIN not set — gain mode is operator-triggered")
	}

	dir, _ := filepath.Abs("../../eval/gain/scenarios")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read scenarios dir: %v", err)
	}
	var paths []string
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".json" {
			paths = append(paths, filepath.Join(dir, e.Name()))
		}
	}
	sort.Strings(paths)
	if len(paths) == 0 {
		t.Fatal("no gain scenarios found")
	}

	settle := 10 * time.Minute
	if v := os.Getenv("STOWAGE_EVAL_SETTLE_TIMEOUT"); v != "" {
		if d, perr := time.ParseDuration(v); perr == nil && d > 0 {
			settle = d
		}
	}

	ctx := context.Background()
	var results []GainResult
	for _, p := range paths {
		sc, err := gain.LoadScenario(p)
		if err != nil {
			t.Fatalf("load scenario %s: %v", p, err)
		}
		// Fresh server per scenario so memories never cross-contaminate retrieval.
		srv := NewTestServer(t, "gain-"+sc.ID)
		if perr := srv.Gateway().Probe(ctx); perr != nil {
			t.Fatalf("gateway probe (check STOWAGE_EVAL_* / OPENROUTER_API_KEY): %v", perr)
		}
		runner := NewRunner(srv, RunConfig{EnableRerank: os.Getenv("STOWAGE_EVAL_RERANK_MODEL") != ""})
		gr, err := RunGainScenario(ctx, srv, runner, srv.Gateway(), sc, settle)
		if err != nil {
			t.Fatalf("gain scenario %s: %v", sc.ID, err)
		}
		t.Logf("GAIN %s [%s]: off=%.2f(%s) on=%.2f(%s) gain=%+.2f",
			gr.ScenarioID, gr.Category, gr.QualityOff, gr.VerdictOff, gr.QualityOn, gr.VerdictOn, gr.Gain)
		results = append(results, gr)
	}

	summary := AggregateGain(results)
	outDir, _ := filepath.Abs("../../eval/results")
	_ = os.MkdirAll(outDir, 0o750)
	out := filepath.Join(outDir, fmt.Sprintf("gain-n%d-%s.jsonl", len(results), time.Now().UTC().Format("20060102T150405Z")))
	w, err := os.Create(out) //nolint:gosec
	if err != nil {
		t.Fatalf("create results: %v", err)
	}
	defer w.Close() //nolint:errcheck
	enc := json.NewEncoder(w)
	for _, r := range results {
		_ = enc.Encode(r)
	}
	_ = enc.Encode(map[string]any{"summary": summary})
	t.Logf("GAIN SUMMARY: mean_gain=%+.4f (on=%.4f off=%.4f) non_negative=%d/%d results=%s",
		summary.MeanGain, summary.MeanQualityOn, summary.MeanQualityOff, summary.NonNegative, summary.Total, out)

	// Release gate (RFC §12): negative aggregate gain fails.
	if summary.MeanGain < 0 {
		t.Errorf("RELEASE GATE: mean gain %.4f < 0 — memory regressed task quality on the standard scenarios", summary.MeanGain)
	}
}
