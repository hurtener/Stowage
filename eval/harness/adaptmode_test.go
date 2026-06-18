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

// TestAdaptMode runs the online-adaptation scenarios with the live gateway (Phase
// 20b, D-078): sequential tasks with the reflection→playbook loop accumulating
// strategies between them. It REPORTS the per-task quality trajectory and the delta
// (last − first); it is not release-gated (the gain delta is the gate). Operator-
// triggered (STOWAGE_EVAL_GATEWAY + STOWAGE_EVAL_GAIN); never CI.
//
//	STOWAGE_EVAL_GATEWAY=bifrost STOWAGE_EVAL_GAIN=1 ... \
//	  go test -tags=fullmode -run TestAdaptMode -timeout 60m ./eval/harness/
func TestAdaptMode(t *testing.T) {
	if os.Getenv("STOWAGE_EVAL_GATEWAY") == "" || os.Getenv("STOWAGE_EVAL_GAIN") == "" {
		t.Skip("STOWAGE_EVAL_GATEWAY / STOWAGE_EVAL_GAIN not set — adaptation mode is operator-triggered")
	}
	dir, _ := filepath.Abs("../../eval/gain/adapt")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read adapt scenarios dir: %v", err)
	}
	var paths []string
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".json" {
			paths = append(paths, filepath.Join(dir, e.Name()))
		}
	}
	sort.Strings(paths)
	if len(paths) == 0 {
		t.Fatal("no adaptation scenarios found")
	}

	budget := 2000
	ctx := context.Background()
	var results []AdaptResult
	for _, p := range paths {
		sc, err := gain.LoadAdaptScenario(p)
		if err != nil {
			t.Fatalf("load adapt scenario %s: %v", p, err)
		}
		srv := NewTestServer(t, "adapt-"+sc.ID)
		if perr := srv.Gateway().Probe(ctx); perr != nil {
			t.Fatalf("gateway probe (check STOWAGE_EVAL_* / OPENROUTER_API_KEY): %v", perr)
		}
		res, err := RunAdaptScenario(ctx, srv, srv.Gateway(), sc, budget)
		if err != nil {
			t.Fatalf("adapt scenario %s: %v", sc.ID, err)
		}
		for _, tr := range res.Tasks {
			t.Logf("ADAPT %s task %d [%s]: q=%.2f(%s) playbook_items=%d", res.ScenarioID, tr.TaskIndex, tr.Outcome, tr.Quality, tr.Verdict, tr.PlaybookItems)
		}
		t.Logf("ADAPT %s: first=%.2f last=%.2f delta=%+.2f", res.ScenarioID, res.FirstQuality, res.LastQuality, res.Delta)
		results = append(results, res)
	}

	outDir, _ := filepath.Abs("../../eval/results")
	_ = os.MkdirAll(outDir, 0o750)
	out := filepath.Join(outDir, fmt.Sprintf("adapt-n%d-%s.jsonl", len(results), time.Now().UTC().Format("20060102T150405Z")))
	w, err := os.Create(out) //nolint:gosec
	if err != nil {
		t.Fatalf("create results: %v", err)
	}
	defer w.Close() //nolint:errcheck
	enc := json.NewEncoder(w)
	for _, r := range results {
		_ = enc.Encode(r)
	}
	t.Logf("ADAPT results written: %s", out)
}
