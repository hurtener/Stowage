package retrieval

import (
	"fmt"
	"strings"
	"time"
)

// RenderMode selects the reader-facing shape Render produces. It is a two-value
// call-site argument — deliberately NOT a config knob (D-141): a knob would
// invite a third surface-specific mode and re-fork the renderer, the exact
// sprawl this phase removes.
type RenderMode int

const (
	// RenderEval reproduces the eval harness's reader-prompt context block,
	// byte-for-byte, as it existed before this phase.
	RenderEval RenderMode = iota
	// RenderMCP is the model-facing MCP body — a superset of RenderEval. In this
	// phase (ae3) its affordance slots (Citation, EpisodeID) are wired but INERT:
	// the base body is identical to RenderEval. ae4a (D-142) is the phase that
	// makes the two modes diverge.
	RenderMCP
)

// RenderItem is the render-input projection. It decouples Render from both
// call-site source types: the server maps retrieval.MemoryItem → RenderItem
// (via RenderItemsFromMemoryItems); eval maps its wire-decoded retrieveItem →
// RenderItem directly. Render never sees a store type or a wire type.
type RenderItem struct {
	Content    string
	OccurredAt int64 // store.Memory.ValidFrom; 0 ⇒ omit the "| When:" suffix
	Stale      bool  // dual-visibility marker (D-105)

	// SupersededByContent and SupersededByDate carry the CURRENT successor's
	// value and assertion date inline on a stale item (D-114, Idea 1). Only
	// meaningful when Stale is true.
	SupersededByContent string
	SupersededByDate    int64

	// RenderMCP affordance slots — populated only on the server path (eval's
	// wire struct carries neither). INERT in ae3: Render emits nothing for
	// them regardless of mode. ae4a (D-142) turns them on for RenderMCP only.
	Citation  string // per-item injection ULID = drill handle (ae4a)
	EpisodeID string // store.Memory.EpisodeID → episode hook (ae4a)
}

// RenderResult exposes both shapes byte-identity requires.
type RenderResult struct {
	// ContextBlock is the assembled CURRENT/SUPERSEDED sections with the
	// [N]/[S1] positional markers — what the eval reader prompt embeds in its
	// user message. RenderEval reproduces it byte-for-byte pre-ae3 behavior.
	ContextBlock string
	// Lines is the per-item display projection (content + "| When:" + inline
	// stale tag) — the equivalent of the eval harness's pre-ae3 `contents`
	// slice, preserved so QuestionResult.Items is unchanged.
	Lines []string
	// CurrentOnly is the raw Content (no date suffix) of non-stale items only —
	// feeds the eval substring-hit metric.
	CurrentOnly []string
}

// staleTag builds the inline "[OUTDATED …]" marker for one stale item. Built
// ONCE here — never re-parsed by a caller (D-141 kills that round-trip).
func staleTag(it RenderItem) string {
	tag := "[OUTDATED — the user later changed this; prefer the current value, use only as history"
	if it.SupersededByContent != "" {
		tag += "; superseded by: " + withDate(it.SupersededByContent, it.SupersededByDate)
	}
	tag += "] "
	return tag
}

// withDate appends the assertion (conversation) date so the reader can do
// temporal reasoning and date-resolve stale values itself (D-109). Daily
// granularity matches the eval dataset.
func withDate(content string, occurredAt int64) string {
	if occurredAt <= 0 {
		return content
	}
	return content + " | When: " + time.UnixMilli(occurredAt).UTC().Format("2006-01-02")
}

// Render is a pure function: no receiver, no package-level mutable state, no
// gateway call (P5). Safe for concurrent reuse (proven under -race).
//
// RenderMCP and RenderEval produce the SAME base body in ae3 — the Citation/
// EpisodeID slots emit nothing in either mode; ae4a is the only phase that
// makes the two modes diverge.
func Render(mode RenderMode, items []RenderItem) RenderResult {
	_ = mode // inert this phase (D-141/D-142) — both modes render identically

	lines := make([]string, 0, len(items))
	currentOnly := make([]string, 0, len(items))
	var current, superseded []string

	for _, it := range items {
		dated := withDate(it.Content, it.OccurredAt)
		if it.Stale {
			// Dual-visibility (D-105) + self-contained successor (D-114): mark the
			// retired value AND name what replaced it and when.
			lines = append(lines, staleTag(it)+dated)
			superseded = append(superseded, dated)
			continue
		}
		lines = append(lines, dated)
		current = append(current, dated)
		currentOnly = append(currentOnly, it.Content) // raw content, no date suffix
	}

	var b strings.Builder
	b.WriteString("CURRENT memories (answer from these):\n")
	if len(current) == 0 {
		b.WriteString("(no current memories retrieved)\n")
	}
	for i, c := range current {
		fmt.Fprintf(&b, "[%d] %s\n", i+1, strings.TrimSpace(c))
	}
	if len(superseded) > 0 {
		b.WriteString("\nSUPERSEDED memories (earlier values the user CHANGED — history only, NEVER answer with these):\n")
		for i, c := range superseded {
			fmt.Fprintf(&b, "[S%d] %s\n", i+1, strings.TrimSpace(c))
		}
	}

	return RenderResult{
		ContextBlock: b.String(),
		Lines:        lines,
		CurrentOnly:  currentOnly,
	}
}

// RenderItemsFromMemoryItems maps the server's retrieval results onto the
// render projection. It carries Citation and Memory.EpisodeID into the
// RenderMCP affordance slots — INERT this phase (ae4a activates them).
func RenderItemsFromMemoryItems(items []MemoryItem) []RenderItem {
	out := make([]RenderItem, len(items))
	for i, it := range items {
		out[i] = RenderItem{
			Content:             it.Memory.Content,
			OccurredAt:          it.Memory.ValidFrom,
			Stale:               it.Stale,
			SupersededByContent: it.SupersededByContent,
			SupersededByDate:    it.SupersededByDate,
			Citation:            it.Citation,
			EpisodeID:           it.Memory.EpisodeID,
		}
	}
	return out
}
