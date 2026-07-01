package retrieval

import (
	"strings"
	"sync"
	"testing"

	"github.com/hurtener/stowage/internal/store"
)

// TestRender_WithDateSuffix pins the "| When: YYYY-MM-DD" date lever (D-109):
// occ<=0 omits the suffix; occ>0 appends it in UTC daily granularity.
func TestRender_WithDateSuffix(t *testing.T) {
	res := Render(RenderEval, []RenderItem{
		{Content: "no date item", OccurredAt: 0},
		{Content: "dated item", OccurredAt: 1684108800000}, // 2023-05-15T00:00:00Z
	})
	if res.Lines[0] != "no date item" {
		t.Errorf("occ<=0 line = %q, want unsuffixed", res.Lines[0])
	}
	want := "dated item | When: 2023-05-15"
	if res.Lines[1] != want {
		t.Errorf("dated line = %q, want %q", res.Lines[1], want)
	}
}

// TestRender_StaleTag pins the inline [OUTDATED …] marker, with and without a
// SupersededByContent successor (D-105/D-114).
func TestRender_StaleTag(t *testing.T) {
	res := Render(RenderEval, []RenderItem{
		{Content: "old value", Stale: true},
		{Content: "old value 2", Stale: true, SupersededByContent: "new value", SupersededByDate: 1684108800000},
	})
	want0 := "[OUTDATED — the user later changed this; prefer the current value, use only as history] old value"
	if res.Lines[0] != want0 {
		t.Errorf("stale line (no successor) = %q, want %q", res.Lines[0], want0)
	}
	want1 := "[OUTDATED — the user later changed this; prefer the current value, use only as history" +
		"; superseded by: new value | When: 2023-05-15] old value 2"
	if res.Lines[1] != want1 {
		t.Errorf("stale line (with successor) = %q, want %q", res.Lines[1], want1)
	}
	// Both items are stale ⇒ CurrentOnly is empty.
	if len(res.CurrentOnly) != 0 {
		t.Errorf("CurrentOnly = %v, want empty (all items stale)", res.CurrentOnly)
	}
}

// TestRender_ContextBlock_Sectioning pins the CURRENT/SUPERSEDED partition and
// the [N]/[S1] positional numbering, driven by the typed Stale bool (no string
// re-parse).
func TestRender_ContextBlock_Sectioning(t *testing.T) {
	res := Render(RenderEval, []RenderItem{
		{Content: "Commute is 45 minutes each way."},
		{Content: "Commute is 30 minutes.", Stale: true},
	})
	want := "CURRENT memories (answer from these):\n" +
		"[1] Commute is 45 minutes each way.\n" +
		"\nSUPERSEDED memories (earlier values the user CHANGED — history only, NEVER answer with these):\n" +
		"[S1] Commute is 30 minutes.\n"
	if res.ContextBlock != want {
		t.Errorf("ContextBlock = %q, want %q", res.ContextBlock, want)
	}
}

// TestRender_EmptyContext renders the explicit no-memories block when no items
// are current.
func TestRender_EmptyContext(t *testing.T) {
	res := Render(RenderEval, nil)
	want := "CURRENT memories (answer from these):\n(no current memories retrieved)\n"
	if res.ContextBlock != want {
		t.Errorf("ContextBlock = %q, want %q", res.ContextBlock, want)
	}
}

// TestRender_GoldenMirrorsReaderPrompt mirrors the eval TestReaderPrompt_Golden
// fixture so the two golden assertions cannot silently diverge (test plan §
// "Golden/unit").
func TestRender_GoldenMirrorsReaderPrompt(t *testing.T) {
	res := Render(RenderEval, []RenderItem{
		{Content: "User spent $60 on coffee mugs."},
		{Content: "  The mugs cost $12 each.  "},
	})
	want := "CURRENT memories (answer from these):\n" +
		"[1] User spent $60 on coffee mugs.\n" +
		"[2] The mugs cost $12 each.\n"
	if res.ContextBlock != want {
		t.Errorf("ContextBlock = %q, want %q", res.ContextBlock, want)
	}
}

// TestRender_ModeDivergence pins AC6 (D-142): RenderMCP and RenderEval now
// DIVERGE over a fixture carrying Citation/EpisodeID — RenderMCP appends the
// drill-handle/episode-hook markers to ContextBlock, RenderEval does not. This
// REVISES ae3's TestRender_ModeDiff (which asserted the two modes were
// byte-identical, back when the slots were inert); the divergence is now the
// intentional, phase-defining behavior. Lines/CurrentOnly stay identical
// across modes either way — markers only ever land in ContextBlock.
func TestRender_ModeDivergence(t *testing.T) {
	fixture := []RenderItem{
		{Content: "current fact", OccurredAt: 1684108800000, Citation: "01ARZ3NDEKTSV4RRFFQ69G5FAV", EpisodeID: "ep-1"},
		{Content: "stale fact", Stale: true, SupersededByContent: "new fact", SupersededByDate: 1684108800000, Citation: "01ARZ3NDEKTSV4RRFFQ69G5FAW", EpisodeID: "ep-2"},
	}
	evalRes := Render(RenderEval, fixture)
	mcpRes := Render(RenderMCP, fixture)

	if evalRes.ContextBlock == mcpRes.ContextBlock {
		t.Errorf("ContextBlock must diverge by mode when Citation/EpisodeID are set (D-142); both were: %q", evalRes.ContextBlock)
	}
	if strings.Contains(evalRes.ContextBlock, "[cite:") || strings.Contains(evalRes.ContextBlock, "[episode:") {
		t.Errorf("RenderEval must never emit drill/episode markers: %q", evalRes.ContextBlock)
	}
	if !strings.Contains(mcpRes.ContextBlock, "[cite:01ARZ3NDEKTSV4RRFFQ69G5FAV]") {
		t.Errorf("RenderMCP missing drill handle for current item: %q", mcpRes.ContextBlock)
	}
	if !strings.Contains(mcpRes.ContextBlock, "[episode:ep-1]") {
		t.Errorf("RenderMCP missing episode hook for current item: %q", mcpRes.ContextBlock)
	}
	if !strings.Contains(mcpRes.ContextBlock, "[cite:01ARZ3NDEKTSV4RRFFQ69G5FAW]") {
		t.Errorf("RenderMCP missing drill handle for superseded item: %q", mcpRes.ContextBlock)
	}
	if !strings.Contains(mcpRes.ContextBlock, "[episode:ep-2]") {
		t.Errorf("RenderMCP missing episode hook for superseded item: %q", mcpRes.ContextBlock)
	}

	// Lines/CurrentOnly are untouched by markers — they feed the eval harness
	// (RenderEval only) and never carry the RenderMCP affordance suffix.
	if len(evalRes.Lines) != len(mcpRes.Lines) {
		t.Fatalf("Lines length differs: eval=%d mcp=%d", len(evalRes.Lines), len(mcpRes.Lines))
	}
	for i := range evalRes.Lines {
		if evalRes.Lines[i] != mcpRes.Lines[i] {
			t.Errorf("Lines[%d] differs by mode: eval=%q mcp=%q", i, evalRes.Lines[i], mcpRes.Lines[i])
		}
		if strings.Contains(mcpRes.Lines[i], "[cite:") || strings.Contains(mcpRes.Lines[i], "[episode:") {
			t.Errorf("Lines[%d] must not carry markers: %q", i, mcpRes.Lines[i])
		}
	}
	if len(evalRes.CurrentOnly) != len(mcpRes.CurrentOnly) {
		t.Fatalf("CurrentOnly length differs: eval=%d mcp=%d", len(evalRes.CurrentOnly), len(mcpRes.CurrentOnly))
	}
}

// TestRender_MCPGolden_LiveSlots pins the exact marker syntax (D-142) so ae4b
// and later phases cannot drift it: [cite:<ULID>] then [episode:<id>],
// space-prefixed, appended after the "| When:" date suffix on each
// CURRENT/SUPERSEDED line.
func TestRender_MCPGolden_LiveSlots(t *testing.T) {
	res := Render(RenderMCP, []RenderItem{
		{Content: "User prefers dark mode.", OccurredAt: 1684108800000, Citation: "01ARZ3NDEKTSV4RRFFQ69G5FAV", EpisodeID: "ep-ui-prefs"},
		{
			Content: "User's commute was 45 minutes.", Stale: true,
			SupersededByContent: "User's commute is now 30 minutes.", SupersededByDate: 1684195200000,
			Citation: "01ARZ3NDEKTSV4RRFFQ69G5FAW", EpisodeID: "ep-commute",
		},
	})
	want := "CURRENT memories (answer from these):\n" +
		"[1] User prefers dark mode. | When: 2023-05-15 [cite:01ARZ3NDEKTSV4RRFFQ69G5FAV] [episode:ep-ui-prefs]\n" +
		"\nSUPERSEDED memories (earlier values the user CHANGED — history only, NEVER answer with these):\n" +
		"[S1] User's commute was 45 minutes. [cite:01ARZ3NDEKTSV4RRFFQ69G5FAW] [episode:ep-commute]\n"
	if res.ContextBlock != want {
		t.Errorf("RenderMCP golden ContextBlock =\n%q\nwant\n%q", res.ContextBlock, want)
	}
}

// TestRender_MCPSlotOmitBoundary pins AC2's boundary (D-142): a Citation with
// no EpisodeID emits the drill handle but omits the episode hook entirely —
// no empty "[episode:]" placeholder, no error.
func TestRender_MCPSlotOmitBoundary(t *testing.T) {
	res := Render(RenderMCP, []RenderItem{
		{Content: "fact with citation only", Citation: "01ARZ3NDEKTSV4RRFFQ69G5FAV"},
	})
	if !strings.Contains(res.ContextBlock, "[cite:01ARZ3NDEKTSV4RRFFQ69G5FAV]") {
		t.Errorf("missing drill handle: %q", res.ContextBlock)
	}
	if strings.Contains(res.ContextBlock, "[episode:") {
		t.Errorf("episode hook must be absent when EpisodeID is empty: %q", res.ContextBlock)
	}

	// And the inverse: EpisodeID with no Citation emits the hook but no handle.
	res2 := Render(RenderMCP, []RenderItem{
		{Content: "fact with episode only", EpisodeID: "ep-only"},
	})
	if strings.Contains(res2.ContextBlock, "[cite:") {
		t.Errorf("drill handle must be absent when Citation is empty: %q", res2.ContextBlock)
	}
	if !strings.Contains(res2.ContextBlock, "[episode:ep-only]") {
		t.Errorf("missing episode hook: %q", res2.ContextBlock)
	}
}

// TestRender_ConcurrentReuse proves Render is a pure function safe for
// concurrent reuse from N goroutines over shared input (§5 concurrency —
// run under -race).
func TestRender_ConcurrentReuse(t *testing.T) {
	fixture := []RenderItem{
		{Content: "fact one", OccurredAt: 1684108800000},
		{Content: "fact two", Stale: true, SupersededByContent: "fact two updated", SupersededByDate: 1684108800000},
		{Content: "fact three"},
	}
	const n = 50
	var wg sync.WaitGroup
	results := make([]RenderResult, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			mode := RenderEval
			if i%2 == 0 {
				mode = RenderMCP
			}
			results[i] = Render(mode, fixture)
		}(i)
	}
	wg.Wait()
	for i := 1; i < n; i++ {
		if results[i].ContextBlock != results[0].ContextBlock {
			t.Errorf("goroutine %d ContextBlock diverged from goroutine 0", i)
		}
	}
}

// TestRender_ConcurrentReuse_PopulatedSlots extends the concurrency proof to the
// live Citation/EpisodeID slots (D-142, ae4a): N goroutines calling Render
// concurrently with RenderMCP over a fixture carrying both markers must all
// produce the byte-identical ContextBlock (run under -race).
func TestRender_ConcurrentReuse_PopulatedSlots(t *testing.T) {
	fixture := []RenderItem{
		{Content: "fact one", OccurredAt: 1684108800000, Citation: "01ARZ3NDEKTSV4RRFFQ69G5FAV", EpisodeID: "ep-1"},
		{Content: "fact two", Stale: true, SupersededByContent: "fact two updated", SupersededByDate: 1684108800000, Citation: "01ARZ3NDEKTSV4RRFFQ69G5FAW", EpisodeID: "ep-2"},
		{Content: "fact three", Citation: "01ARZ3NDEKTSV4RRFFQ69G5FAX"},
	}
	const n = 50
	var wg sync.WaitGroup
	results := make([]RenderResult, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = Render(RenderMCP, fixture)
		}(i)
	}
	wg.Wait()
	for i := 1; i < n; i++ {
		if results[i].ContextBlock != results[0].ContextBlock {
			t.Errorf("goroutine %d ContextBlock diverged from goroutine 0 (populated slots)", i)
		}
	}
	if !strings.Contains(results[0].ContextBlock, "[cite:01ARZ3NDEKTSV4RRFFQ69G5FAV]") ||
		!strings.Contains(results[0].ContextBlock, "[episode:ep-1]") {
		t.Errorf("expected markers missing from concurrent result: %q", results[0].ContextBlock)
	}
}

// TestRenderReadBody_ComposesMapperAndRender pins the ae4a composition helper
// (D-142): RenderReadBody(items) == Render(RenderMCP, RenderItemsFromMemoryItems(items)).ContextBlock,
// and its signature (no Store, no ctx) is itself the no-new-query proof.
func TestRenderReadBody_ComposesMapperAndRender(t *testing.T) {
	items := []MemoryItem{
		{
			Memory:   store.Memory{Content: "hello", ValidFrom: 1684108800000, EpisodeID: "ep-7"},
			Citation: "cite-1",
		},
	}
	got := RenderReadBody(items)
	want := Render(RenderMCP, RenderItemsFromMemoryItems(items)).ContextBlock
	if got != want {
		t.Errorf("RenderReadBody = %q, want %q", got, want)
	}
	if !strings.Contains(got, "[cite:cite-1]") || !strings.Contains(got, "[episode:ep-7]") {
		t.Errorf("RenderReadBody missing expected markers: %q", got)
	}
}

// TestRenderReadBody_Empty pins the empty-items case (no crash, sentinel body).
func TestRenderReadBody_Empty(t *testing.T) {
	got := RenderReadBody(nil)
	want := "CURRENT memories (answer from these):\n(no current memories retrieved)\n"
	if got != want {
		t.Errorf("RenderReadBody(nil) = %q, want %q", got, want)
	}
}

// TestRenderReadBody_Degraded proves the render step needs nothing from the
// gateway (D-036/AC7): a "degraded" retrieval Response's Items still render
// with hooks/handles intact — Render never inspects Response.Degraded, it only
// ever sees the Items slice already carried on it.
func TestRenderReadBody_Degraded(t *testing.T) {
	degraded := &Response{
		Degraded: true,
		Items: []MemoryItem{
			{Memory: store.Memory{Content: "lexical-only fact", EpisodeID: "ep-degraded"}, Citation: "cite-degraded"},
		},
	}
	got := RenderReadBody(degraded.Items)
	if !strings.Contains(got, "[cite:cite-degraded]") || !strings.Contains(got, "[episode:ep-degraded]") {
		t.Errorf("degraded response body missing expected markers: %q", got)
	}
}

// TestRenderItemsFromMemoryItems_CarriesSlots pins that the server-path mapper
// carries Citation and Memory.EpisodeID into the RenderMCP affordance slots
// (inert in ae3, activated in ae4a).
func TestRenderItemsFromMemoryItems_CarriesSlots(t *testing.T) {
	items := []MemoryItem{
		{
			Memory:              store.Memory{Content: "hello", ValidFrom: 42, EpisodeID: "ep-7"},
			Citation:            "cite-1",
			Stale:               true,
			SupersededByContent: "hello updated",
			SupersededByDate:    43,
		},
	}
	out := RenderItemsFromMemoryItems(items)
	if len(out) != 1 {
		t.Fatalf("len(out) = %d, want 1", len(out))
	}
	got := out[0]
	if got.Content != "hello" || got.OccurredAt != 42 || !got.Stale ||
		got.SupersededByContent != "hello updated" || got.SupersededByDate != 43 ||
		got.Citation != "cite-1" || got.EpisodeID != "ep-7" {
		t.Errorf("RenderItemsFromMemoryItems mapped = %+v, want all fields carried", got)
	}
}
