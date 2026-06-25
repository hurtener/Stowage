//go:build fullmode

package harness

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// TestExtractAssistantSpecifics is a CHEAP, targeted validation (operator-run, paid, never CI)
// for the assistant-provided-info magnet (D-123 follow-up): instead of re-learning all 100
// questions to check whether specific assistant-stated details get memorized, it ingests a
// handful of tiny conversations where the ASSISTANT states a specific (a chess move, a
// recommended ingredient/brand, a numeric result) and asserts those specifics land as memories.
// Run it after editing the assistant magnet / extraction prompt:
//
//	STOWAGE_EVAL_GATEWAY=bifrost STOWAGE_EVAL_MODEL=openai/gpt-5.4-mini \
//	  go test -tags=fullmode -run TestExtractAssistantSpecifics -v ./eval/harness/
func TestExtractAssistantSpecifics(t *testing.T) {
	if os.Getenv("STOWAGE_EVAL_GATEWAY") == "" {
		t.Skip("set STOWAGE_EVAL_GATEWAY (+STOWAGE_EVAL_MODEL) to run this paid extraction probe")
	}
	srv := NewTestServer(t, "extract-assistant-"+newIDish())
	runner := NewRunner(srv, RunConfig{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	if err := SeedEvalTopics(ctx, srv); err != nil {
		t.Fatalf("seed topics: %v", err)
	}

	// Each case: a short user→assistant exchange where the assistant states a SPECIFIC that a
	// later "what did you recommend / what was the move / what was the result" question needs.
	cases := []struct {
		id, want string
		turns    [][2]string // {role, content}
	}{
		{"chess", "Kg3", [][2]string{
			{"user", "Let's analyze our chess game. It's move 28 and my king is exposed on g1 — what should I play?"},
			{"assistant", "Given the exposed king, the best move is 28. Kg3, stepping the king toward safety and connecting your rooks."},
		}},
		{"cocktail", "Absinthe", [][2]string{
			{"user", "I'm stocking a home cocktail bar. Recommend 5 bottles to cover classic cocktails."},
			{"assistant", "For five versatile bottles I recommend: London dry gin, rye whiskey, sweet vermouth, Campari, and a bottle of Absinthe for classics like the Sazerac."},
		}},
		{"brand", "Veja", [][2]string{
			{"user", "Any sustainable sneaker brands that use natural materials?"},
			{"assistant", "Yes — Veja is a strong choice; their sneakers use wild rubber sourced from the Amazon rainforest."},
		}},
		{"framerate", "20%", [][2]string{
			{"user", "How much did the Hardware-Aware Modular pipeline improve framerate in your benchmark?"},
			{"assistant", "In the benchmark the Hardware-Aware Modular pipeline delivered an average framerate improvement of approximately 20%."},
		}},
	}

	for _, c := range cases {
		conv := &ConvFixture{ID: "asst-" + c.id, Sessions: []SessionFixture{{ID: "asst-" + c.id + "-s0"}}}
		for _, tn := range c.turns {
			conv.Sessions[0].Turns = append(conv.Sessions[0].Turns, TurnFixture{Role: tn[0], Content: tn[1]})
		}
		if _, err := runner.ingestConversation(ctx, conv); err != nil {
			t.Fatalf("ingest %s: %v", c.id, err)
		}
		if err := runner.flushBuffer(ctx, conv.ID); err != nil {
			t.Fatalf("flush %s: %v", c.id, err)
		}
	}
	if err := srv.WaitForQuiescence(ctx, 2*time.Minute); err != nil {
		t.Fatalf("settle: %v", err)
	}

	// Pull every active memory's content and check which specifics were captured.
	mems, _, err := srv.Store.Memories().ListByStatus(ctx, srv.Scope(), "active", 500, "")
	if err != nil {
		t.Fatalf("list active: %v", err)
	}
	all := make([]string, 0, len(mems))
	for _, m := range mems {
		all = append(all, m.Content)
	}
	blob := strings.ToLower(strings.Join(all, "\n"))
	t.Logf("extracted %d active memories from %d assistant cases", len(mems), len(cases))

	captured := 0
	for _, c := range cases {
		if strings.Contains(blob, strings.ToLower(c.want)) {
			captured++
			t.Logf("  CAPTURED  [%s] %q", c.id, c.want)
		} else {
			t.Logf("  MISSED    [%s] %q", c.id, c.want)
		}
	}
	// Validation bar: the magnet is working if it captures most assistant specifics. Lenient
	// by one (LLM variance); the per-case log above is the real signal for iterating.
	if captured < len(cases)-1 {
		t.Errorf("assistant magnet weak: captured %d/%d specifics (want >= %d)", captured, len(cases), len(cases)-1)
	}
}

// newIDish returns a short unique-ish suffix without importing a ULID lib here (the tenant just
// needs to be distinct per run). Uses the wall clock; fine for a one-shot operator probe.
func newIDish() string {
	return strings.ReplaceAll(time.Now().UTC().Format("150405.000"), ".", "")
}
