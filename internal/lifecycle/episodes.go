package lifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/hurtener/stowage/internal/causal"
	"github.com/hurtener/stowage/internal/episodes"
	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/reconcile"
	"github.com/hurtener/stowage/internal/store"
)

const (
	episodeDetectLockKey  int64 = 0x1407
	episodeNarrateLockKey int64 = 0x1408
	// episodeRecordCap bounds how many records of a session an episode draws on.
	episodeRecordCap = 1000
)

// runDetectEpisodes is the Phase-22 boundary-detection sweep (gateway-free, D-079):
// per tenant scope it finds closed sessions without an episode and creates one (or
// more, on a temporal gap split) per session. Idempotent: a session that already
// has an episode is skipped.
func (m *Manager) runDetectEpisodes(ctx context.Context) {
	if !m.episodesOn() {
		return
	}
	release, err := m.st.Ops().AdvisoryLock(ctx, episodeDetectLockKey)
	if err != nil {
		m.log.WarnContext(ctx, "lifecycle/episode-detect: advisory lock failed", "err", err)
		return
	}
	defer func() { _ = release() }()

	tenants, err := m.st.Tenants(ctx)
	if err != nil {
		m.log.WarnContext(ctx, "lifecycle/episode-detect: list tenants failed", "err", err)
		return
	}
	idleBefore := time.Now().Add(-m.profile.EpisodeIdleWindow).UnixMilli()
	gapMs := m.profile.EpisodeGapSplit.Milliseconds()
	now := time.Now().UnixMilli()

	for _, tenant := range tenants {
		scope := identity.Scope{Tenant: tenant}
		sessions, err := m.st.Records().DistinctSessions(ctx, scope, idleBefore, m.profile.EpisodeBatchSize)
		if err != nil {
			m.log.WarnContext(ctx, "lifecycle/episode-detect: distinct sessions failed", "tenant", tenant, "err", err)
			continue
		}
		for _, si := range sessions {
			// Create episodes at the FULL scope (project/user from the session), not
			// tenant-only — so narratives are retrievable for the owning user and two
			// users sharing a session_id never merge (P3, D-079).
			sessScope := identity.Scope{Tenant: tenant, Project: si.ProjectID, User: si.UserID}
			// Idempotency gate: skip a session that already has an episode (D-079).
			if _, gerr := m.st.Episodes().GetEpisodeBySession(ctx, sessScope, si.SessionID); gerr == nil {
				continue
			} else if !errors.Is(gerr, store.ErrNotFound) {
				m.log.WarnContext(ctx, "lifecycle/episode-detect: get-by-session failed", "err", gerr)
				continue
			}
			recs := m.loadSessionRecords(ctx, sessScope, si.SessionID)
			for _, d := range episodes.DetectEpisodes(recs, gapMs) {
				ep := store.Episode{
					ID: ulid.Make().String(), SessionID: si.SessionID, Status: "closed",
					StartedAt: d.StartedAt, EndedAt: d.EndedAt, Outcome: d.Outcome,
					CreatedAt: now, UpdatedAt: now,
				}
				if cerr := m.st.Episodes().CreateEpisode(ctx, sessScope, ep); cerr != nil {
					m.log.WarnContext(ctx, "lifecycle/episode-detect: create episode failed", "err", cerr)
				}
			}
		}
	}
}

// runNarrateEpisodes is the Phase-22 narration sweep: for each episode lacking a
// narrative, it loads the episode's records, constructs a narrative via the gateway
// (schema-constrained), commits a narrative memory (episode_id + provenance), and
// attaches it. Idempotent: narrated episodes are skipped; transient gateway errors
// leave the episode for the next sweep.
func (m *Manager) runNarrateEpisodes(ctx context.Context) {
	if !m.episodesOn() {
		return
	}
	release, err := m.st.Ops().AdvisoryLock(ctx, episodeNarrateLockKey)
	if err != nil {
		m.log.WarnContext(ctx, "lifecycle/episode-narrate: advisory lock failed", "err", err)
		return
	}
	defer func() { _ = release() }()

	eps, err := m.st.Episodes().ListEpisodesNeedingNarrative(ctx, m.profile.EpisodeBatchSize)
	if err != nil {
		m.log.WarnContext(ctx, "lifecycle/episode-narrate: list failed", "err", err)
		return
	}
	for _, ep := range eps {
		// User-scope the narrative (not session) so it is retrievable across the
		// user's sessions (Phase-23 episodic retrieval), with the episode's full
		// project/user from the episode row (P3, D-079).
		scope := identity.Scope{Tenant: ep.TenantID, Project: ep.ProjectID, User: ep.UserID}
		recs := m.episodeRecords(ctx, scope, ep)
		if len(recs) == 0 {
			continue
		}
		narr, nerr := episodes.Narrate(ctx, m.gw, recs)
		if nerr != nil {
			m.log.WarnContext(ctx, "lifecycle/episode-narrate: narrate failed; will retry", "episode", ep.ID, "err", nerr)
			continue
		}
		memID := ulid.Make().String()
		now := time.Now().UnixMilli()
		hash := reconcile.ContentHash(reconcile.NormalizeContent(narr.Narrative))
		prov := make([]store.Provenance, 0, len(recs))
		for _, r := range recs {
			prov = append(prov, store.Provenance{MemoryID: memID, RecordID: r.ID, SpanStart: 0, SpanEnd: len(r.Content), TenantID: ep.TenantID})
		}
		mem := store.Memory{
			ID: memID, Kind: "narrative", Content: narr.Narrative, Context: narr.Title,
			Status: "active", Importance: 3, Confidence: 0.8, TrustSource: "episodic",
			Stability: 1.0, EpisodeID: ep.ID, ContentHash: hash,
			CreatedAt: now, UpdatedAt: now,
		}
		// Phase 24 (D-083): infer causal led_to edges among the episode's decisions and
		// commit them ATOMICALLY with the narrative (one CommitSet) — runs exactly once
		// per episode (gated by the narrative-creation that follows). Best-effort: a
		// gateway/inference failure leaves the episode narrated without edges.
		links, events := m.inferCausalLinks(ctx, scope, ep, recs, narr.Narrative, now)
		cerr := m.st.Memories().Commit(ctx, scope, store.CommitSet{Action: store.ActionAdd, Memory: mem, Provenance: prov, Links: links, Events: events, Scope: scope})
		if errors.Is(cerr, store.ErrDuplicateContent) {
			// A memory with this narrative already exists (e.g. a prior sweep
			// committed it but crashed before linking). Recover idempotently: link
			// the existing memory to the episode instead of stranding it (D-079).
			if existing, gerr := m.st.Memories().GetByContentHash(ctx, scope, hash); gerr == nil {
				memID = existing.ID
				// The whole CommitSet (incl. any inferred links) rolled back on the
				// dup. On crash-recovery the original sweep already committed identical
				// edges; on a rare distinct-episode hash collision the edges are a
				// no-edge outcome (D-083 best-effort) — log so it is not silent.
				if len(links) > 0 {
					m.log.InfoContext(ctx, "lifecycle/causal: narrative dedup — inferred edges not committed this pass", "episode", ep.ID, "edges", len(links))
				}
			} else {
				m.log.WarnContext(ctx, "lifecycle/episode-narrate: duplicate content but lookup failed", "episode", ep.ID, "err", gerr)
				continue
			}
		} else if cerr != nil {
			m.log.WarnContext(ctx, "lifecycle/episode-narrate: commit narrative failed", "episode", ep.ID, "err", cerr)
			continue
		}
		if serr := m.st.Episodes().SetEpisodeNarrative(ctx, scope, ep.ID, memID, narr.Title, now); serr != nil {
			m.log.WarnContext(ctx, "lifecycle/episode-narrate: set narrative failed", "episode", ep.ID, "err", serr)
		}
	}
}

// inferCausalLinks gathers the episode's decision-class memories (reverse provenance
// over the episode's records), asks the gateway to propose led_to edges grounded in
// the narrative, confidence-gates them, and returns the store.Link rows + one
// causal.inferred audit event per edge (RFC §5.6/§6b, D-083). Best-effort: any failure
// (gateway down, <2 decisions, gather error) returns no links — narration still
// commits. Source="inferred"; type="led_to" (canonical cause→effect).
func (m *Manager) inferCausalLinks(ctx context.Context, scope identity.Scope, ep store.Episode, recs []store.Record, narrative string, now int64) ([]store.Link, []store.Event) {
	if m.gw == nil {
		return nil, nil
	}
	recordIDs := make([]string, 0, len(recs))
	for _, r := range recs {
		recordIDs = append(recordIDs, r.ID)
	}
	mems, err := m.st.Memories().ListMemoriesByRecords(ctx, scope, recordIDs, causal.DecisionKinds)
	if err != nil {
		m.log.WarnContext(ctx, "lifecycle/causal: gather decisions failed", "episode", ep.ID, "err", err)
		return nil, nil
	}
	if len(mems) < 2 {
		return nil, nil
	}
	cands := make([]causal.Candidate, len(mems))
	for i, mm := range mems {
		cands[i] = causal.Candidate{ID: mm.ID, Kind: mm.Kind, Content: mm.Content}
	}
	props, ierr := causal.Infer(ctx, m.gw, narrative, cands)
	if ierr != nil {
		m.log.WarnContext(ctx, "lifecycle/causal: infer failed; narrating without edges", "episode", ep.ID, "err", ierr)
		return nil, nil
	}
	gated := causal.GateProposals(props, len(cands), m.profile.CausalMinConfidence)
	if len(gated) == 0 {
		return nil, nil
	}
	links := make([]store.Link, 0, len(gated))
	events := make([]store.Event, 0, len(gated))
	for _, p := range gated {
		from, to := cands[p.FromIdx].ID, cands[p.ToIdx].ID
		links = append(links, store.Link{
			ID: ulid.Make().String(), TenantID: ep.TenantID,
			FromMemory: from, ToMemory: to, Type: "led_to", Source: "inferred",
			Confidence: p.Confidence, CreatedAt: now,
		})
		payload, _ := json.Marshal(struct {
			To         string  `json:"to"`
			Confidence float64 `json:"confidence"`
			Reason     string  `json:"reason"`
			EpisodeID  string  `json:"episode_id"`
		}{To: to, Confidence: p.Confidence, Reason: p.Reason, EpisodeID: ep.ID})
		events = append(events, store.Event{
			ID: ulid.Make().String(), TenantID: ep.TenantID, ProjectID: ep.ProjectID, UserID: ep.UserID,
			Type: "causal.inferred", SubjectID: from, Reason: "inferred led_to edge", Payload: string(payload),
			CreatedAt: now,
		})
	}
	return links, events
}

// loadSessionRecords loads up to episodeRecordCap of a session's records (all
// branches), ordered by occurred_at — the boundary-detection input.
func (m *Manager) loadSessionRecords(ctx context.Context, scope identity.Scope, sessionID string) []store.Record {
	recs, _, err := m.st.Records().ListBySession(ctx, scope, sessionID, "", episodeRecordCap, "")
	if err != nil {
		m.log.WarnContext(ctx, "lifecycle/episode: load session records failed", "session", sessionID, "err", err)
		return nil
	}
	return recs
}

// episodeRecords loads the records belonging to an episode: its session's records
// within the episode's [StartedAt, EndedAt] time range.
func (m *Manager) episodeRecords(ctx context.Context, scope identity.Scope, ep store.Episode) []store.Record {
	all := m.loadSessionRecords(ctx, scope, ep.SessionID)
	out := make([]store.Record, 0, len(all))
	for _, r := range all {
		if r.OccurredAt >= ep.StartedAt && r.OccurredAt <= ep.EndedAt {
			out = append(out, r)
		}
	}
	return out
}
