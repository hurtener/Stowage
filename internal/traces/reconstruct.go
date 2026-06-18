// Package traces implements the Phase-26 reasoning-trace export (RFC §6c, D-086):
// the read-only, per-response_id memory-into-conclusion chain (query, injected
// memories, drill-down spans, typed links, verification verdicts) reconstructed from
// the day-one tables and exported as an optionally ed25519-signed bundle.
//
// reconstruct.go is the gateway-free assembly core; sign.go adds the detached
// signature. The package imports no gateway (the trace is deterministic read-assembly).
package traces

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/hurtener/stowage/internal/excerpt"
	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/store"
)

// Event types the capture paths emit (keyed by response_id as the event SubjectID).
const (
	EventRetrieveQuery = "retrieve.query"
	EventVerifyVerdict = "verify.verdict"
)

// maxTraceEvents caps the events scanned per response (resource guard). Events are
// returned newest-first; the single retrieve.query event is the oldest response-keyed
// event, so a response that accumulates more than this many verify.verdict events
// would push the query outside the window. The cap is sized far above any realistic
// per-response verdict count (verdicts are user-initiated, rare per response) so this
// is effectively unreachable; it is a documented bound, not silent loss.
const maxTraceEvents = 1000

// TraceSpan is one drill-down provenance span (P1).
type TraceSpan struct {
	RecordID  string `json:"record_id"`
	SpanStart int    `json:"span_start,omitempty"`
	SpanEnd   int    `json:"span_end,omitempty"`
	Excerpt   string `json:"excerpt,omitempty"`
}

// TraceLink is one typed edge out of an injected memory.
type TraceLink struct {
	To         string  `json:"to"`
	Type       string  `json:"type"`
	Confidence float64 `json:"confidence,omitempty"`
}

// TraceItem is one injected memory and its chain.
type TraceItem struct {
	MemoryID   string      `json:"memory_id"`
	Kind       string      `json:"kind"`
	Content    string      `json:"content"`
	Status     string      `json:"status"`
	Rank       int         `json:"rank"`
	Score      float64     `json:"score"`
	Lane       string      `json:"lane,omitempty"`
	WasCited   bool        `json:"was_cited,omitempty"`
	Feedback   string      `json:"feedback,omitempty"`
	Provenance []TraceSpan `json:"provenance,omitempty"`
	Links      []TraceLink `json:"links,omitempty"`
}

// TraceVerdict is one verification verdict run against the response.
type TraceVerdict struct {
	Claim      string  `json:"claim"`
	Verdict    string  `json:"verdict"`
	Confidence float64 `json:"confidence,omitempty"`
	Degraded   bool    `json:"degraded,omitempty"`
}

// Trace is the full reasoning chain for one response_id.
type Trace struct {
	ResponseID  string         `json:"response_id"`
	Query       string         `json:"query,omitempty"`
	Support     string         `json:"support,omitempty"`
	Degraded    bool           `json:"degraded,omitempty"`
	Items       []TraceItem    `json:"items"`
	Verdicts    []TraceVerdict `json:"verdicts,omitempty"`
	GeneratedAt int64          `json:"generated_at"`
}

// Reconstruct assembles the reasoning trace for responseID from the day-one tables
// (injections + events + records/provenance + links), scope-enforced (P3). An unknown
// response_id yields an empty trace (no error). Deterministic + gateway-free. now is
// the caller-supplied generation timestamp (kept out of the package for testability).
func Reconstruct(ctx context.Context, st store.Store, scope identity.Scope, responseID string, now int64) (Trace, error) {
	tr := Trace{ResponseID: responseID, Items: []TraceItem{}, GeneratedAt: now}
	if responseID == "" {
		return tr, nil
	}

	injs, err := st.Injections().ListByResponse(ctx, scope, responseID)
	if err != nil {
		return Trace{}, fmt.Errorf("traces: list injections: %w", err)
	}

	for _, inj := range injs {
		item := TraceItem{
			MemoryID: inj.MemoryID, Rank: inj.Rank, Score: inj.Score,
			Lane: inj.Lane, WasCited: inj.WasCited, Feedback: inj.Feedback,
		}
		mem, mErr := st.Memories().Get(ctx, scope, inj.MemoryID)
		if mErr == nil && mem != nil {
			item.Kind, item.Content, item.Status = mem.Kind, mem.Content, mem.Status
			item.Provenance = provenanceSpans(ctx, st, scope, inj.MemoryID)
			item.Links = outLinks(ctx, st, scope, inj.MemoryID)
		}
		// A memory deleted since injection still appears (it WAS used) — with empty
		// kind/content/status, so the audit shows the gap rather than hiding it.
		tr.Items = append(tr.Items, item)
	}

	applyResponseEvents(ctx, st, scope, responseID, &tr)
	return tr, nil
}

// provenanceSpans returns the drill-down spans for a memory (record excerpt per span).
func provenanceSpans(ctx context.Context, st store.Store, scope identity.Scope, memID string) []TraceSpan {
	j, err := st.Memories().GetJunctions(ctx, scope, memID)
	if err != nil || len(j.Provenance) == 0 {
		return nil
	}
	recIDs := make([]string, 0, len(j.Provenance))
	seen := map[string]bool{}
	for _, p := range j.Provenance {
		if !seen[p.RecordID] {
			seen[p.RecordID] = true
			recIDs = append(recIDs, p.RecordID)
		}
	}
	recs, _ := st.Records().GetMany(ctx, scope, recIDs)
	byID := make(map[string]store.Record, len(recs))
	for _, r := range recs {
		byID[r.ID] = r
	}
	out := make([]TraceSpan, 0, len(j.Provenance))
	for _, p := range j.Provenance {
		span := TraceSpan{RecordID: p.RecordID, SpanStart: p.SpanStart, SpanEnd: p.SpanEnd}
		if r, ok := byID[p.RecordID]; ok {
			span.Excerpt = excerpt.Clamp(r.Content, p.SpanStart, p.SpanEnd)
		}
		out = append(out, span)
	}
	return out
}

// outLinks returns the typed edges originating at a memory.
func outLinks(ctx context.Context, st store.Store, scope identity.Scope, memID string) []TraceLink {
	links, err := st.Memories().ListLinks(ctx, scope, memID, "")
	if err != nil || len(links) == 0 {
		return nil
	}
	out := make([]TraceLink, 0, len(links))
	for _, l := range links {
		if l.FromMemory != memID {
			continue // ListLinks(from,"") matches from OR to; keep only out-edges
		}
		out = append(out, TraceLink{To: l.ToMemory, Type: l.Type, Confidence: l.Confidence})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// applyResponseEvents folds the response-keyed events (retrieve.query, verify.verdict)
// into the trace. Events are keyed by SubjectID = responseID (D-086 capture).
func applyResponseEvents(ctx context.Context, st store.Store, scope identity.Scope, responseID string, tr *Trace) {
	evs, err := st.Events().ListBySubject(ctx, scope, responseID, maxTraceEvents)
	if err != nil {
		return // best-effort: the chain is still valid without the captured signals
	}
	for _, e := range evs {
		switch e.Type {
		case EventRetrieveQuery:
			var p struct {
				Query    string `json:"query"`
				Support  string `json:"support"`
				Degraded bool   `json:"degraded"`
			}
			if json.Unmarshal([]byte(e.Payload), &p) == nil {
				tr.Query, tr.Support, tr.Degraded = p.Query, p.Support, p.Degraded
			}
		case EventVerifyVerdict:
			var p struct {
				Claim      string  `json:"claim"`
				Verdict    string  `json:"verdict"`
				Confidence float64 `json:"confidence"`
				Degraded   bool    `json:"degraded"`
			}
			if json.Unmarshal([]byte(e.Payload), &p) == nil {
				tr.Verdicts = append(tr.Verdicts, TraceVerdict{Claim: p.Claim, Verdict: p.Verdict, Confidence: p.Confidence, Degraded: p.Degraded})
			}
		}
	}
}
