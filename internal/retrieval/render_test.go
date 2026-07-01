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

// TestRender_ModeDiff pins AC4: RenderMCP and RenderEval produce the identical
// base body over a shared fixture in ae3 — the Citation/EpisodeID slots are
// wired but inert until ae4a.
func TestRender_ModeDiff(t *testing.T) {
	fixture := []RenderItem{
		{Content: "current fact", OccurredAt: 1684108800000, Citation: "01ARZ3NDEKTSV4RRFFQ69G5FAV", EpisodeID: "ep-1"},
		{Content: "stale fact", Stale: true, SupersededByContent: "new fact", SupersededByDate: 1684108800000, Citation: "01ARZ3NDEKTSV4RRFFQ69G5FAW", EpisodeID: "ep-2"},
	}
	evalRes := Render(RenderEval, fixture)
	mcpRes := Render(RenderMCP, fixture)
	if evalRes.ContextBlock != mcpRes.ContextBlock {
		t.Errorf("ContextBlock differs by mode:\neval: %q\nmcp:  %q", evalRes.ContextBlock, mcpRes.ContextBlock)
	}
	if len(evalRes.Lines) != len(mcpRes.Lines) {
		t.Fatalf("Lines length differs: eval=%d mcp=%d", len(evalRes.Lines), len(mcpRes.Lines))
	}
	for i := range evalRes.Lines {
		if evalRes.Lines[i] != mcpRes.Lines[i] {
			t.Errorf("Lines[%d] differs by mode: eval=%q mcp=%q", i, evalRes.Lines[i], mcpRes.Lines[i])
		}
	}
	if len(evalRes.CurrentOnly) != len(mcpRes.CurrentOnly) {
		t.Fatalf("CurrentOnly length differs: eval=%d mcp=%d", len(evalRes.CurrentOnly), len(mcpRes.CurrentOnly))
	}
}

// TestRender_SlotsInert pins AC5: a fixture carrying Citation/EpisodeID renders
// no citation handle or episode hook anywhere in the output, in RenderMCP, in
// ae3 (ae4a is the phase that activates them).
func TestRender_SlotsInert(t *testing.T) {
	fixture := []RenderItem{
		{Content: "fact with handles", Citation: "01ARZ3NDEKTSV4RRFFQ69G5FAV", EpisodeID: "ep-42"},
	}
	res := Render(RenderMCP, fixture)
	for _, blob := range append([]string{res.ContextBlock}, res.Lines...) {
		if strings.Contains(blob, "01ARZ3NDEKTSV4RRFFQ69G5FAV") {
			t.Errorf("Citation leaked into render output in ae3: %q", blob)
		}
		if strings.Contains(blob, "ep-42") {
			t.Errorf("EpisodeID leaked into render output in ae3: %q", blob)
		}
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
