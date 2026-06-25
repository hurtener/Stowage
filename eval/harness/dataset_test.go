package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/hurtener/stowage/eval/datasets"
)

// TestDatasetRegistry proves the public benchmarks resolve through the registry
// (D-096) — LoCoMo and longmemeval_s were previously built but unselectable.
func TestDatasetRegistry(t *testing.T) {
	for _, name := range []string{"longmemeval", "longmemeval_s", "locomo"} {
		spec, ok := datasets.Lookup(name)
		if !ok {
			t.Fatalf("dataset %q not registered (registry: %v)", name, datasets.Names())
		}
		if spec.Normalize == nil || spec.Fetch == nil || spec.DataFile == "" {
			t.Errorf("dataset %q spec incomplete: %+v", name, spec)
		}
	}
	// MustLookup surfaces the known set on an unknown name.
	if _, err := datasets.MustLookup("nope"); err == nil {
		t.Error("MustLookup(nope) should error")
	}
}

// TestRunDataset_Wiring proves an arbitrary normalized dataset flows through the
// single RunDataset path end-to-end — ingest → (mock) extract → retrieve → score —
// with the mock gateway and a scripted extraction (no paid call). This is the
// deterministic proof that LoCoMo/longmemeval_s are now genuinely runnable through
// the runner (D-096); the benchmark NUMBERS remain operator-run (full mode).
func TestRunDataset_Wiring(t *testing.T) {
	srv := NewTestServer(t, "eval-dataset-wiring")
	runner := NewRunner(srv, RunConfig{})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// A tiny one-conversation dataset whose answer ("Neovim") is extractable from a turn.
	convs := []datasets.Conversation{{
		ID: "wire-conv-1",
		Sessions: []datasets.Session{{
			ID: "wire-sess-1",
			Turns: []datasets.Turn{
				{Role: "user", Content: "I want to tell you about my setup."},
				{Role: "user", Content: "My code editor of choice is Neovim and I use it daily."},
			},
		}},
	}}
	questions := []datasets.Question{{
		ID: "wire-q-1", Text: "what code editor do I use", ConvID: "wire-conv-1",
		Expected: datasets.Expected{Answer: "Neovim"},
	}}

	// BeforeFlush pushes a deterministic extraction producing one memory containing
	// the answer, with provenance pointing at a real ingested record ID.
	beforeFlush := func(_ string, recordIDs []string) error {
		if len(recordIDs) == 0 {
			return fmt.Errorf("no record IDs to cite")
		}
		entry, err := json.Marshal(map[string]any{
			"candidates": []map[string]any{{
				"kind":                "preference",
				"content":             "The user's code editor of choice is Neovim.",
				"context":             "Developer tooling preference",
				"entities":            []string{"Neovim"},
				"keywords":            []string{"Neovim", "code editor", "editor"},
				"anticipated_queries": []string{"what code editor do I use", "what is my editor"},
				"importance":          3,
				"confidence":          0.95,
				"provenance":          []map[string]any{{"record_id": recordIDs[len(recordIDs)-1], "span_start": 0, "span_end": 30}},
			}},
		})
		if err != nil {
			return err
		}
		srv.PushExtractionScript(entry)
		return nil
	}

	res, err := RunDataset(ctx, srv, runner, convs, questions, RunDatasetOpts{
		Limit:       1,
		Judge:       false,            // deterministic answer_context_hit only — no gateway Complete for judging
		Settle:      90 * time.Second, // headroom for CI -race + coverage instrumentation (avoids quiescence flake)
		BeforeFlush: beforeFlush,
	})
	if err != nil {
		t.Fatalf("RunDataset: %v", err)
	}
	if len(res.Results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(res.Results))
	}
	if !res.Results[0].Hit {
		t.Errorf("question should hit the extracted memory containing %q; got items=%v",
			questions[0].Expected.Answer, res.Results[0].Items)
	}
	if srv.ActiveMemoryCount(ctx) < 1 {
		t.Error("expected at least one committed memory from the scripted extraction")
	}
}
