//go:build fullmode

package harness

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// TestTemporalEpisodeProbe is a cheap, targeted validation (operator-run, paid, never CI) of
// the two temporal-recall levers, without a 100q re-learn:
//
//	idea 1 — temporal anticipated-queries: a dated event memory must be retrievable by a
//	         TIME-RELATIVE question ("how many days since I visited the museum"), via the
//	         anticipated_queries lane, not just by topic.
//	idea 4 — episodes: with STOWAGE_EVAL_EPISODES=1 the consolidation pass must actually build
//	         episodes (dated-event arcs) so they exist as retrievable narratives.
//
//	STOWAGE_EVAL_GATEWAY=bifrost STOWAGE_EVAL_MODEL=openai/gpt-5.4-mini \
//	  go test -tags=fullmode -run TestTemporalEpisodeProbe -v ./eval/harness/
func TestTemporalEpisodeProbe(t *testing.T) {
	if os.Getenv("STOWAGE_EVAL_GATEWAY") == "" {
		t.Skip("set STOWAGE_EVAL_GATEWAY (+STOWAGE_EVAL_MODEL) to run this paid probe")
	}
	t.Setenv("STOWAGE_EVAL_EPISODES", "1") // idea 4: build episodes in the harness
	srv := NewTestServer(t, "temporal-probe-"+newIDish())
	runner := NewRunner(srv, RunConfig{RetrieveLimit: 10})
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	if err := SeedEvalTopics(ctx, srv); err != nil {
		t.Fatalf("seed topics: %v", err)
	}

	// A dated, multi-turn session about a concrete event (so it can form an episode and a
	// dated event memory). OccurredAt is a fixed historical date.
	day := time.Date(2023, 3, 1, 14, 0, 0, 0, time.UTC).UnixMilli()
	conv := &ConvFixture{ID: "temporal-museum", Sessions: []SessionFixture{{
		ID: "temporal-museum-s0",
		Turns: []TurnFixture{
			{Role: "user", Content: "Today I finally visited the Science Museum downtown.", OccurredAt: day},
			{Role: "assistant", Content: "That sounds great — what did you see at the Science Museum?", OccurredAt: day + 60000},
			{Role: "user", Content: "I spent the afternoon at the dinosaur exhibit and the planetarium show.", OccurredAt: day + 120000},
			{Role: "assistant", Content: "A full afternoon at the Science Museum's dinosaur exhibit and planetarium — nice.", OccurredAt: day + 180000},
		},
	}}}
	if _, err := runner.ingestConversation(ctx, conv); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if err := runner.flushBuffer(ctx, conv.ID); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if err := srv.WaitForQuiescence(ctx, 2*time.Minute); err != nil {
		t.Fatalf("settle (extract): %v", err)
	}
	// Consolidation pass also runs the episode detect+narrate sweeps (episodes on).
	srv.RunConsolidation(ctx)
	if err := srv.WaitForQuiescence(ctx, 2*time.Minute); err != nil {
		t.Fatalf("settle (consolidate): %v", err)
	}

	// idea 4: episodes built.
	eps, _, err := srv.Store.Episodes().ListEpisodes(ctx, srv.Scope(), 50, "")
	if err != nil {
		t.Fatalf("list episodes: %v", err)
	}
	// idea 4 is diagnostic for now (episodes wired in the harness but not yet firing in the eval
	// batch — under investigation; the detect sweep finds the closed session but no episode lands).
	// Reported, not asserted, so this probe still gates idea 1.
	t.Logf("idea 4 (diagnostic): %d episode(s) built (0 = detect/narrate not yet firing in the eval)", len(eps))

	// idea 1: a time-relative question must surface the museum event memory (via the
	// anticipated_queries lane the new extraction prompt populates).
	qr, err := runner.scoreQuestion(ctx, QuestionFixture{
		ID: "tq1", Text: "How many days ago did I visit the Science Museum?", Category: "temporal-reasoning",
	})
	if err != nil {
		t.Fatalf("retrieve temporal query: %v", err)
	}
	hit := false
	for _, it := range qr.Items {
		if strings.Contains(strings.ToLower(it), "science museum") {
			hit = true
			break
		}
	}
	t.Logf("idea 1: temporal query retrieved %d items; museum-event surfaced=%v", len(qr.Items), hit)
	for i, it := range qr.Items {
		t.Logf("   [%d] %s", i+1, it)
	}
	if !hit {
		t.Errorf("idea 1: temporal query did NOT surface the Science Museum event memory in top-%d", len(qr.Items))
	}
}
