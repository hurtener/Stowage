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
	// RenderMCP is the model-facing MCP body — a superset of RenderEval. Per
	// item it appends a drill handle ([cite:<Citation>]) whenever Citation is
	// set, and an episode hook ([episode:<EpisodeID>]) whenever EpisodeID is
	// set (D-142, ae4a). RenderEval never emits these — the eval harness's
	// wire-decoded items carry neither field, and only RenderMCP's branch
	// appends them.
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
	// wire struct carries neither). Active for RenderMCP only (D-142, ae4a):
	// RenderEval's branch never emits them, regardless of whether they're set.
	Citation  string // per-item injection ULID = drill handle (D-142)
	EpisodeID string // store.Memory.EpisodeID → episode hook (D-142)
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

// renderMarkers builds the RenderMCP-only affordance suffix for one item: a
// drill handle ([cite:<ULID>]) whenever Citation is set, followed by an
// episode hook ([episode:<id>]) whenever EpisodeID is set (D-142, ae4a). Each
// present marker is space-prefixed so it reads as a trailing annotation on the
// item's ContextBlock line. RenderEval returns "" unconditionally — the two
// modes diverge starting here (ae3's base bodies were identical; this is the
// one phase that makes them differ). Both slots fail soft by construction: an
// empty field simply omits that marker, never an error.
func renderMarkers(mode RenderMode, it RenderItem) string {
	if mode != RenderMCP {
		return ""
	}
	var b strings.Builder
	if it.Citation != "" {
		b.WriteString(" [cite:" + it.Citation + "]")
	}
	if it.EpisodeID != "" {
		b.WriteString(" [episode:" + it.EpisodeID + "]")
	}
	return b.String()
}

// Render is a pure function: no receiver, no package-level mutable state, no
// gateway call (P5). Safe for concurrent reuse (proven under -race).
//
// RenderEval reproduces the pre-ae3 eval reader-prompt body byte-for-byte.
// RenderMCP is a superset: it appends the drill-handle/episode-hook markers
// (renderMarkers) to each CURRENT/SUPERSEDED line (D-142, ae4a) — the one
// divergence between the two modes.
func Render(mode RenderMode, items []RenderItem) RenderResult {
	lines := make([]string, 0, len(items))
	currentOnly := make([]string, 0, len(items))
	var current, superseded []string
	// currentMarkers/supersededMarkers are index-aligned with current/superseded —
	// the RenderMCP-only affordance suffix for that item ("" for RenderEval, or
	// when neither slot is set). Lines/CurrentOnly are untouched by markers: they
	// feed the eval harness (RenderEval only), which carries neither slot.
	var currentMarkers, supersededMarkers []string

	for _, it := range items {
		dated := withDate(it.Content, it.OccurredAt)
		marker := renderMarkers(mode, it)
		if it.Stale {
			// Dual-visibility (D-105) + self-contained successor (D-114): mark the
			// retired value AND name what replaced it and when.
			lines = append(lines, staleTag(it)+dated)
			superseded = append(superseded, dated)
			supersededMarkers = append(supersededMarkers, marker)
			continue
		}
		lines = append(lines, dated)
		current = append(current, dated)
		currentMarkers = append(currentMarkers, marker)
		currentOnly = append(currentOnly, it.Content) // raw content, no date suffix
	}

	var b strings.Builder
	b.WriteString("CURRENT memories (answer from these):\n")
	if len(current) == 0 {
		b.WriteString("(no current memories retrieved)\n")
	}
	for i, c := range current {
		fmt.Fprintf(&b, "[%d] %s%s\n", i+1, strings.TrimSpace(c), currentMarkers[i])
	}
	if len(superseded) > 0 {
		b.WriteString("\nSUPERSEDED memories (earlier values the user CHANGED — history only, NEVER answer with these):\n")
		for i, c := range superseded {
			fmt.Fprintf(&b, "[S%d] %s%s\n", i+1, strings.TrimSpace(c), supersededMarkers[i])
		}
	}

	return RenderResult{
		ContextBlock: b.String(),
		Lines:        lines,
		CurrentOnly:  currentOnly,
	}
}

// RenderReadBody renders the model-facing lean markdown body for a retrieval
// response. It is the ONE place the RenderMCP mode and the MemoryItem→RenderItem
// mapper are composed, so every surface (MCP Text, HTTP rendered, SDK Rendered)
// emits a byte-identical reader body (D-067/D-073). Pure: no receiver, no store,
// no ctx, no gateway call (D-036 gateway-free; the source data is already loaded
// on the Response) — that signature is the no-new-query proof for the episode
// hook (D-142, ae4a): it reads item.Memory.EpisodeID, already populated by the
// retrieval GetMany, so no new store query is issued.
func RenderReadBody(items []MemoryItem) string {
	return Render(RenderMCP, RenderItemsFromMemoryItems(items)).ContextBlock
}

// RenderItemsFromMemoryItems maps the server's retrieval results onto the
// render projection. It carries Citation and Memory.EpisodeID into the
// RenderMCP affordance slots (D-142, ae4a activates them for RenderMCP only).
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
