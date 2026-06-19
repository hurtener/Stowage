package proactive

// engine.go — the Evaluate orchestrator (RFC §6d, D-087). It runs the enabled
// trigger rules, scores each candidate with the SAME machinery as retrieval
// (scoring.Score), applies the per-(scope,trigger_class) feedback multiplier,
// gates by the governance threshold + budget, dedupes against the session's
// existing offers, persists survivors as pending suggestions (+ events), and
// returns the offers with the degraded flag. No new scoring logic — proactive
// offers are scored exactly like retrieved memories so a noisy/decayed memory
// cannot be louder when pushed than when pulled.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/hurtener/stowage/internal/episodes"
	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/scoring"
	"github.com/hurtener/stowage/internal/store"
	"github.com/oklog/ulid/v2"
)

// Offer is one surfaced proactive suggestion, returned to the caller and persisted
// as a pending suggestion row. Content carries the offered memory's text inline so
// the agent can act on the offer without a second round-trip (the offer IS the
// volunteered context, not a pointer to it).
type Offer struct {
	ID          string  `json:"id"`
	TriggerKind string  `json:"trigger_kind"`
	MemoryID    string  `json:"memory_id"`
	EpisodeID   string  `json:"episode_id,omitempty"`
	Title       string  `json:"title"`
	Content     string  `json:"content"`
	Score       float64 `json:"score"`
}

// ErrSessionRequired is returned by Evaluate when no session id is supplied: the
// per-session dedupe (the primary anti-spam defence) is keyed on it, so evaluating
// without one would re-offer the same context every call. Surfaces map it to 400.
var ErrSessionRequired = errors.New("proactive: session_id is required to evaluate suggestions")

// Evaluate produces the proactive offers for one session turn. It is the single
// logic core behind every surface (HTTP/MCP/SDK GET suggestions). cfg is the
// already-resolved governance for scope (see Resolve); now is unix ms.
//
// Returns the surfaced offers (≤ cfg.Budget, score-desc), a degraded flag (true
// when the similar_episode rule ran without the gateway), and an error only for a
// hard store failure — a per-rule failure degrades that rule, it does not fail the
// turn.
func Evaluate(ctx context.Context, st store.Store, searcher episodes.NarrativeSearcher, scope identity.Scope, sessionID, query string, cfg Config, now int64) ([]Offer, bool, error) {
	if sessionID == "" {
		// Per-session dedupe is the primary anti-spam defence; without a session id it
		// would silently re-offer the same context every call. Fail loud, don't spam.
		return nil, false, ErrSessionRequired
	}
	if !cfg.Enabled || cfg.Budget <= 0 {
		return nil, false, nil // opted out — silence over spam
	}

	// 1. Gather candidates from each enabled rule. A rule error degrades that
	//    class (logged via the returned degraded flag for similar) but never
	//    aborts the turn — proactive is best-effort by construction (P2 spirit).
	var cands []Candidate
	degraded := false

	if cfg.classEnabled(ClassRecentEpisode) {
		if c, err := recentEpisodeCandidates(ctx, st, scope, now); err == nil {
			cands = append(cands, c...)
		}
	}
	if cfg.classEnabled(ClassSimilarEpisode) {
		c, deg, err := similarEpisodeCandidates(ctx, st, searcher, scope, query)
		degraded = degraded || deg
		if err == nil {
			cands = append(cands, c...)
		}
	}
	if cfg.classEnabled(ClassExpiring) {
		if c, err := expiringCandidates(ctx, st, scope, now); err == nil {
			cands = append(cands, c...)
		}
	}
	if len(cands) == 0 {
		return nil, degraded, nil
	}

	// 2. Per-class feedback multiplier (one query per class actually present).
	//    The tally is windowed to the trailing feedbackWindowMs so old dismissals age
	//    out — a class the scope stopped disliking recovers instead of being silenced
	//    forever (RFC §6d "triggers that annoy decay" is recoverable, not permanent).
	multByClass, err := classMultipliers(ctx, st.Suggestions(), scope, cands, now-feedbackWindowMs)
	if err != nil {
		return nil, degraded, err
	}

	// 3. Score each candidate with the retrieval scorer. FusedScore = the rule's
	//    pre-utility relevance, so a proactively-offered memory is subject to the
	//    same use/noise/decay/trust shaping as a pulled one, then knocked down by
	//    the class's accept/dismiss confidence.
	type scored struct {
		cand    Candidate
		final   float64
		content string // the offered memory's text, carried inline (UX: no round-trip)
		title   string
	}
	out := make([]scored, 0, len(cands))
	for _, cand := range cands {
		mem, gerr := st.Memories().Get(ctx, scope, cand.MemoryID)
		if gerr != nil || mem == nil {
			continue // offered memory vanished (reconciled/forgotten) between rule and score
		}
		base, _ := scoring.Score(scoring.Inputs{
			Memory:      memoryFacts(mem),
			FusedScore:  cand.Relevance,
			Now:         now,
			SameSession: sessionID == mem.SessionID,
		})
		final := base * multByClass[cand.TriggerKind]
		if final < cfg.Threshold {
			continue // below the scope's governance bar
		}
		// Title falls back to a content snippet when the source has none (e.g. an
		// untitled episode) so an offer is never a bare id with no label.
		title := cand.Title
		if title == "" {
			title = truncate(mem.Content, 64)
		}
		out = append(out, scored{cand: cand, final: final, content: mem.Content, title: title})
	}
	if len(out) == 0 {
		return nil, degraded, nil
	}

	// 4. Rank score-desc, stable on (trigger_kind, memory_id) so equal scores are
	//    deterministic across drivers.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].final != out[j].final {
			return out[i].final > out[j].final
		}
		if out[i].cand.TriggerKind != out[j].cand.TriggerKind {
			return out[i].cand.TriggerKind < out[j].cand.TriggerKind
		}
		return out[i].cand.MemoryID < out[j].cand.MemoryID
	})

	// 5. Dedupe against this session's existing offers (any status) so we never
	//    re-surface the same memory the user already accepted, dismissed, or saw
	//    pending — and dedupe within this batch.
	seen, err := sessionOfferedMemories(ctx, st.Suggestions(), scope, sessionID)
	if err != nil {
		return nil, degraded, err
	}

	// 6. Trim to budget, build offers + pending rows.
	offers := make([]Offer, 0, cfg.Budget)
	rows := make([]store.Suggestion, 0, cfg.Budget)
	for _, s := range out {
		if len(offers) >= cfg.Budget {
			break
		}
		if seen[s.cand.MemoryID] {
			continue
		}
		seen[s.cand.MemoryID] = true
		id := ulid.Make().String()
		offers = append(offers, Offer{
			ID: id, TriggerKind: s.cand.TriggerKind, MemoryID: s.cand.MemoryID,
			EpisodeID: s.cand.EpisodeID, Title: s.title, Content: s.content, Score: s.final,
		})
		rows = append(rows, store.Suggestion{
			ID: id, TenantID: scope.Tenant, ProjectID: scope.Project, UserID: scope.User,
			SessionID:   sessionID,
			TriggerKind: s.cand.TriggerKind, MemoryID: s.cand.MemoryID, EpisodeID: s.cand.EpisodeID,
			Status: "pending", CreatedAt: now, UpdatedAt: now,
		})
	}
	if len(rows) == 0 {
		return nil, degraded, nil
	}

	// 7. Persist pending rows, then emit one suggestion.offered event per offer.
	//    Persistence is the durable record; the events are the audit trail (§8).
	if err := st.Suggestions().Create(ctx, scope, rows); err != nil {
		return nil, degraded, fmt.Errorf("proactive: persist suggestions: %w", err)
	}
	emitOffered(ctx, st, scope, sessionID, offers, now)

	return offers, degraded, nil
}

// feedbackWindowMs bounds how far back accept/dismiss feedback counts toward a
// class's confidence multiplier. Feedback older than this ages out, so a class the
// scope kept dismissing months ago is not suppressed forever — it gets a fresh
// chance once the recent tally clears (the recovery path RFC §6d implies).
const feedbackWindowMs = int64(30 * 24 * 60 * 60 * 1000) // 30 days

// classMultipliers computes the feedback multiplier for each trigger class present
// in cands (one CountByTrigger query per distinct class), counting only feedback at
// or after `since`.
func classMultipliers(ctx context.Context, ss store.SuggestionStore, scope identity.Scope, cands []Candidate, since int64) (map[string]float64, error) {
	out := make(map[string]float64, 3)
	for _, c := range cands {
		if _, ok := out[c.TriggerKind]; ok {
			continue
		}
		acc, dis, err := ss.CountByTrigger(ctx, scope, c.TriggerKind, since)
		if err != nil {
			return nil, fmt.Errorf("proactive: count %s: %w", c.TriggerKind, err)
		}
		out[c.TriggerKind] = classMultiplier(acc, dis)
	}
	return out, nil
}

// sessionOfferedMemories returns the set of memory IDs already offered in this
// session (any status), so the engine never re-surfaces a memory the session has
// already seen. sessionID is non-empty by the time this runs (Evaluate rejects an
// empty session up front).
func sessionOfferedMemories(ctx context.Context, ss store.SuggestionStore, scope identity.Scope, sessionID string) (map[string]bool, error) {
	prior, err := ss.ListBySession(ctx, scope, sessionID, "", dedupeScanLimit)
	if err != nil {
		return nil, fmt.Errorf("proactive: load session suggestions: %w", err)
	}
	seen := make(map[string]bool, len(prior))
	for _, p := range prior {
		seen[p.MemoryID] = true
	}
	return seen, nil
}

// dedupeScanLimit bounds the per-session dedupe scan.
const dedupeScanLimit = 200

// emitOffered emits one suggestion.offered event per surfaced offer. Emission is
// best-effort — a failed emit must not fail the turn (the rows are already durable).
func emitOffered(ctx context.Context, st store.Store, scope identity.Scope, sessionID string, offers []Offer, now int64) {
	for _, o := range offers {
		payload, _ := json.Marshal(struct {
			TriggerKind string  `json:"trigger_kind"`
			MemoryID    string  `json:"memory_id"`
			EpisodeID   string  `json:"episode_id,omitempty"`
			Score       float64 `json:"score"`
		}{o.TriggerKind, o.MemoryID, o.EpisodeID, o.Score})
		_ = st.Events().Emit(ctx, scope, store.Event{
			ID: ulid.Make().String(), SessionID: sessionID,
			Type: "suggestion.offered", SubjectID: o.ID,
			Reason: "proactive offer surfaced", Payload: string(payload), CreatedAt: now,
		})
	}
}

// memoryFacts projects a stored memory into the scoring inputs (mirrors the
// retrieval layer's mapping so proactive and retrieval score identically).
func memoryFacts(m *store.Memory) scoring.MemoryFacts {
	return scoring.MemoryFacts{
		MatchCount: m.MatchCount, InjectCount: m.InjectCount, UseCount: m.UseCount,
		SaveCount: m.SaveCount, FailCount: m.FailCount, NoiseCount: m.NoiseCount,
		Importance: m.Importance, Confidence: m.Confidence, TrustSource: m.TrustSource,
		Stability: m.Stability, CreatedAt: m.CreatedAt, LastAccessedAt: m.LastAccessedAt,
		SessionID: m.SessionID,
	}
}
