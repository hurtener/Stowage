package api

// Phase 18 — rollback & pending-confirmation resolution (D-064, D-065).
//
// Endpoints:
//   GET  /v1/memories/{id}          — read memory + junctions + ancestor chain
//   POST /v1/memories/{id}/rollback — restore from prior-state event payload
//   PATCH /v1/memories/{id}         — confirm or reject a parked memory

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/reconcile"
	"github.com/hurtener/stowage/internal/store"
)

// --- wire types ------------------------------------------------------------

// memoryResponse is the wire format for GET /v1/memories/{id}.
type memoryResponse struct {
	Memory          memoryJSON    `json:"memory"`
	Entities        []string      `json:"entities"`
	Keywords        []string      `json:"keywords"`
	Queries         []string      `json:"queries"`
	Provenance      []provRefJSON `json:"provenance,omitempty"`
	SupersedesChain []string      `json:"supersedes_chain,omitempty"` // up to 10 ancestor IDs
}

// memoryJSON is the JSON representation of a store.Memory.
type memoryJSON struct {
	ID             string  `json:"id"`
	Kind           string  `json:"kind"`
	Content        string  `json:"content"`
	Context        string  `json:"context,omitempty"`
	Status         string  `json:"status"`
	Importance     int     `json:"importance"`
	Confidence     float64 `json:"confidence"`
	TrustSource    string  `json:"trust_source"`
	MatchCount     int64   `json:"match_count"`
	InjectCount    int64   `json:"inject_count"`
	UseCount       int64   `json:"use_count"`
	SaveCount      int64   `json:"save_count"`
	FailCount      int64   `json:"fail_count,omitempty"`
	NoiseCount     int64   `json:"noise_count,omitempty"`
	Stability      float64 `json:"stability"`
	ValidFrom      int64   `json:"valid_from,omitempty"`
	ValidUntil     int64   `json:"valid_until,omitempty"`
	EpisodeID      string  `json:"episode_id,omitempty"`
	SupersedesID   string  `json:"supersedes_id,omitempty"`
	SupersededByID string  `json:"superseded_by_id,omitempty"`
	PrivacyZone    string  `json:"privacy_zone,omitempty"`
	ContentHash    string  `json:"content_hash,omitempty"`
	CreatedAt      int64   `json:"created_at"`
	UpdatedAt      int64   `json:"updated_at"`
}

// provRefJSON is a compact provenance reference (matches reconcile.provRefJSON).
type provRefJSON struct {
	RecordID  string `json:"record_id"`
	SpanStart int    `json:"span_start,omitempty"`
	SpanEnd   int    `json:"span_end,omitempty"`
}

// patchMemoryRequest is the wire format for PATCH /v1/memories/{id}.
type patchMemoryRequest struct {
	Action string `json:"action"` // "confirm" | "reject"
}

// priorStatePayload is the D-017 prior-state JSON parsed by the rollback endpoint.
// It mirrors reconcile.priorStateJSON (independent copy to avoid cross-package import).
type priorStatePayload struct {
	ID          string        `json:"id"`
	Kind        string        `json:"kind"`
	Content     string        `json:"content"`
	Context     string        `json:"context,omitempty"`
	Status      string        `json:"status"`
	Importance  int           `json:"importance"`
	Confidence  float64       `json:"confidence"`
	TrustSource string        `json:"trust_source"`
	MatchCount  int64         `json:"match_count"`
	InjectCount int64         `json:"inject_count"`
	UseCount    int64         `json:"use_count"`
	SaveCount   int64         `json:"save_count"`
	FailCount   int64         `json:"fail_count,omitempty"`
	NoiseCount  int64         `json:"noise_count,omitempty"`
	Stability   float64       `json:"stability"`
	ValidFrom   int64         `json:"valid_from,omitempty"`
	ValidUntil  int64         `json:"valid_until,omitempty"`
	EpisodeID   string        `json:"episode_id,omitempty"`
	PrivacyZone string        `json:"privacy_zone,omitempty"`
	ContentHash string        `json:"content_hash,omitempty"`
	CreatedAt   int64         `json:"created_at"`
	UpdatedAt   int64         `json:"updated_at"`
	Entities    []string      `json:"entities,omitempty"`
	Keywords    []string      `json:"keywords,omitempty"`
	Queries     []string      `json:"queries,omitempty"`
	Provenance  []provRefJSON `json:"provenance,omitempty"`
}

// --- GET /v1/memories/{id} --------------------------------------------------

// handleGetMemory returns the memory, its junction rows, and up to 10 ancestors
// in the supersedes chain (oldest-last order — same as chain walk direction).
func (s *Server) handleGetMemory(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		respondJSON(w, http.StatusBadRequest, errBody("id is required"))
		return
	}

	authKey := keyFromContext(r.Context())
	scope := identity.Scope{Tenant: authKey.TenantID}
	ctx := r.Context()

	mem, err := s.st.Memories().Get(ctx, scope, id)
	if err != nil {
		if isNotFound(err) {
			respondJSON(w, http.StatusNotFound, errBody("memory not found"))
			return
		}
		s.log.ErrorContext(ctx, "api: get memory", "id", id, "err", err)
		respondJSON(w, http.StatusInternalServerError, errBody("internal error"))
		return
	}

	jt, err := s.st.Memories().GetJunctions(ctx, scope, id)
	if err != nil {
		s.log.WarnContext(ctx, "api: get memory junctions", "id", id, "err", err)
		// Non-fatal — return memory with empty junctions.
	}

	// Walk the supersedes chain (depth-capped at 10).
	chain := s.walkSupersedesChain(ctx, scope, mem.SupersedesID, 10)

	resp := memoryResponse{
		Memory:          memoryToJSON(mem),
		Entities:        jt.Entities,
		Keywords:        jt.Keywords,
		Queries:         jt.Queries,
		SupersedesChain: chain,
	}
	for _, p := range jt.Provenance {
		resp.Provenance = append(resp.Provenance, provRefJSON{
			RecordID:  p.RecordID,
			SpanStart: p.SpanStart,
			SpanEnd:   p.SpanEnd,
		})
	}

	respondJSON(w, http.StatusOK, resp)
}

// walkSupersedesChain follows supersedes_id links from startID up to maxDepth
// hops, returning the collected IDs in walk order (parent-first).
func (s *Server) walkSupersedesChain(ctx context.Context, scope identity.Scope, startID string, maxDepth int) []string {
	if startID == "" || maxDepth <= 0 {
		return nil
	}
	seen := make(map[string]bool)
	var chain []string
	cur := startID
	for i := 0; i < maxDepth && cur != ""; i++ {
		if seen[cur] {
			break // cycle guard
		}
		seen[cur] = true
		chain = append(chain, cur)
		ancestor, err := s.st.Memories().Get(ctx, scope, cur)
		if err != nil {
			break // ancestor gone or scope mismatch — stop walking
		}
		cur = ancestor.SupersedesID
	}
	return chain
}

// --- POST /v1/memories/{id}/rollback ----------------------------------------

// handleRollbackMemory restores a memory to the state recorded in the most
// recent D-017 prior-state event (memory.updated / memory.merged /
// memory.superseded) for {id}. Contract: D-064.
//
// Conflict guards (409):
//   - already_rolled_back: a memory.rolled_back event is newer than the newest
//     restorable event (double-rollback guard).
//   - invalid_prior_state: snapshot payload fails to parse or prior.ID != id.
//   - downstream_conflict: result row has been superseded downstream (chain must
//     unwind newest-first).
//   - incomplete_snapshots: at least one merge sibling lacks a parseable snapshot.
//   - no_prior_state: no restorable event found at all.
func (s *Server) handleRollbackMemory(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		respondJSON(w, http.StatusBadRequest, errBody("id is required"))
		return
	}

	authKey := keyFromContext(r.Context())
	scope := identity.Scope{Tenant: authKey.TenantID}
	ctx := r.Context()

	// Fetch current memory state (404 guard + pre-rollback snapshot source).
	mem, err := s.st.Memories().Get(ctx, scope, id)
	if err != nil {
		if isNotFound(err) {
			respondJSON(w, http.StatusNotFound, errBody("memory not found"))
			return
		}
		s.log.ErrorContext(ctx, "api: rollback get memory", "id", id, "err", err)
		respondJSON(w, http.StatusInternalServerError, errBody("internal error"))
		return
	}

	// --- Scan event log (newest-first, limit 50) ----------------------------
	events, err := s.st.Events().ListBySubject(ctx, scope, id, 50)
	if err != nil {
		s.log.ErrorContext(ctx, "api: rollback list events", "id", id, "err", err)
		respondJSON(w, http.StatusInternalServerError, errBody("internal error"))
		return
	}

	// Restorable event types (D-064).
	isRestorable := func(evType string) bool {
		return evType == "memory.updated" || evType == "memory.merged" || evType == "memory.superseded"
	}

	// Walk newest-first. The first restorable event wins; if a rolled_back
	// event appears before any restorable event the call is a double-rollback.
	var invertibleEvent *store.Event
	for i := range events {
		ev := &events[i]
		if ev.Type == "memory.rolled_back" {
			// A rollback event is newer than any restorable event → double rollback.
			respondJSON(w, http.StatusConflict, map[string]string{
				"error": "already_rolled_back: memory has already been rolled back",
				"code":  "already_rolled_back",
			})
			return
		}
		if isRestorable(ev.Type) {
			invertibleEvent = ev
			break
		}
	}
	if invertibleEvent == nil {
		respondJSON(w, http.StatusConflict, map[string]string{
			"error": "no_prior_state: no restorable prior state found in event log",
			"code":  "no_prior_state",
		})
		return
	}

	// --- Parse the prior-state snapshot -------------------------------------
	prior, parseErr := parsePriorState(invertibleEvent.Payload)
	if parseErr != nil || prior.ID != id {
		respondJSON(w, http.StatusConflict, map[string]string{
			"error": "invalid_prior_state: snapshot payload is missing or has wrong id",
			"code":  "invalid_prior_state",
		})
		return
	}

	now := time.Now().UnixMilli()

	// --- Dispatch on event type ----------------------------------------------

	switch invertibleEvent.Type {
	case "memory.updated":
		// Restore {id} in place from snapshot. No tombstone.
		s.commitUpdateRollback(w, ctx, scope, id, mem, prior, now)

	case "memory.superseded":
		// result row = current mem.SupersededByID.
		// Downstream conflict guard: result row must still be active (or not
		// superseded further) to allow rollback — chain unwinds newest-first.
		if mem.SupersededByID == "" {
			// No result row to tombstone; plain restore of {id}.
			s.commitUpdateRollback(w, ctx, scope, id, mem, prior, now)
			return
		}
		resultRow, fetchErr := s.st.Memories().Get(ctx, scope, mem.SupersededByID)
		if fetchErr != nil && !isNotFound(fetchErr) {
			s.log.ErrorContext(ctx, "api: rollback fetch result row", "id", mem.SupersededByID, "err", fetchErr)
			respondJSON(w, http.StatusInternalServerError, errBody("internal error"))
			return
		}
		if fetchErr == nil && resultRow.Status != "active" {
			respondJSON(w, http.StatusConflict, map[string]string{
				"error": "downstream_conflict: result row has been modified; unwind the chain newest-first",
				"code":  "downstream_conflict",
			})
			return
		}

		// Fetch pre-rollback junctions for the memory.rolled_back event payload.
		preJT, _ := s.st.Memories().GetJunctions(ctx, scope, id)

		restored := priorToMemory(prior, now)
		cs := store.CommitSet{
			Action:     store.ActionRollback,
			Memory:     restored,
			Entities:   prior.Entities,
			Keywords:   prior.Keywords,
			Queries:    prior.Queries,
			Provenance: buildProvenance(prior, scope, now),
			Events: []store.Event{{
				ID:        ulid.Make().String(),
				Type:      "memory.rolled_back",
				SubjectID: id,
				Reason:    "api: rollback superseded op",
				Payload:   reconcile.MarshalPriorState(*mem, preJT),
				CreatedAt: now,
			}},
			Scope: scope,
		}
		if fetchErr == nil {
			cs.Targets = []store.Memory{*resultRow}
		}
		s.doCommitRollback(w, ctx, scope, id, cs)

	case "memory.merged":
		// Merge rollback: find ALL siblings (same superseded_by_id), restore all
		// atomically in ONE CommitSet. Every sibling must have a snapshot.
		supersederID := mem.SupersededByID
		if supersederID == "" {
			// No superseder recorded; restore {id} alone.
			s.commitUpdateRollback(w, ctx, scope, id, mem, prior, now)
			return
		}

		// Downstream conflict guard on the digest.
		digestRow, fetchErr := s.st.Memories().Get(ctx, scope, supersederID)
		if fetchErr != nil && !isNotFound(fetchErr) {
			s.log.ErrorContext(ctx, "api: rollback fetch digest", "id", supersederID, "err", fetchErr)
			respondJSON(w, http.StatusInternalServerError, errBody("internal error"))
			return
		}
		if fetchErr == nil && digestRow.Status != "active" {
			respondJSON(w, http.StatusConflict, map[string]string{
				"error": "downstream_conflict: digest has been modified; unwind newest-first",
				"code":  "downstream_conflict",
			})
			return
		}

		// Discover ALL siblings (including {id}).
		allSiblings, listErr := s.st.Memories().ListSupersededBy(ctx, scope, supersederID)
		if listErr != nil {
			s.log.ErrorContext(ctx, "api: rollback list siblings", "superseder", supersederID, "err", listErr)
			respondJSON(w, http.StatusInternalServerError, errBody("internal error"))
			return
		}
		// Ensure {id} is included in the sibling list (it should be, but guard).
		if len(allSiblings) == 0 {
			allSiblings = []store.Memory{*mem}
		}

		// Each sibling needs its own prior-state snapshot. Collect them.
		type siblingSnap struct {
			mem   store.Memory
			prior *priorStatePayload
			jt    store.MemoryJunctions
		}
		var snaps []siblingSnap
		for _, sib := range allSiblings {
			sibEvents, evErr := s.st.Events().ListBySubject(ctx, scope, sib.ID, 50)
			if evErr != nil {
				s.log.ErrorContext(ctx, "api: rollback list sibling events", "id", sib.ID, "err", evErr)
				respondJSON(w, http.StatusInternalServerError, errBody("internal error"))
				return
			}
			var sibPrior *priorStatePayload
			for _, ev := range sibEvents {
				if ev.Type == "memory.merged" {
					p, pErr := parsePriorState(ev.Payload)
					if pErr == nil && p.ID == sib.ID {
						sibPrior = p
						break
					}
				}
			}
			if sibPrior == nil {
				respondJSON(w, http.StatusConflict, map[string]string{
					"error": "incomplete_snapshots: sibling " + sib.ID + " lacks a parseable snapshot",
					"code":  "incomplete_snapshots",
				})
				return
			}
			jt, _ := s.st.Memories().GetJunctions(ctx, scope, sib.ID)
			snaps = append(snaps, siblingSnap{mem: sib, prior: sibPrior, jt: jt})
		}

		// Build CommitSet. Primary = first sibling ({id} or first in list).
		// Extra = remaining siblings.
		primary := snaps[0]
		restoredPrimary := priorToMemory(primary.prior, now)
		events := []store.Event{{
			ID:        ulid.Make().String(),
			Type:      "memory.rolled_back",
			SubjectID: primary.mem.ID,
			Reason:    "api: rollback merged op",
			Payload:   reconcile.MarshalPriorState(primary.mem, primary.jt),
			CreatedAt: now,
		}}
		var extras []store.RollbackMemory
		for _, snap := range snaps[1:] {
			extras = append(extras, store.RollbackMemory{
				Memory:     priorToMemory(snap.prior, now),
				Entities:   snap.prior.Entities,
				Keywords:   snap.prior.Keywords,
				Queries:    snap.prior.Queries,
				Provenance: buildProvenance(snap.prior, scope, now),
			})
			events = append(events, store.Event{
				ID:        ulid.Make().String(),
				Type:      "memory.rolled_back",
				SubjectID: snap.mem.ID,
				Reason:    "api: rollback merged op (sibling)",
				Payload:   reconcile.MarshalPriorState(snap.mem, snap.jt),
				CreatedAt: now,
			})
		}

		cs := store.CommitSet{
			Action:        store.ActionRollback,
			Memory:        restoredPrimary,
			Entities:      primary.prior.Entities,
			Keywords:      primary.prior.Keywords,
			Queries:       primary.prior.Queries,
			Provenance:    buildProvenance(primary.prior, scope, now),
			ExtraMemories: extras,
			Events:        events,
			Scope:         scope,
		}
		if fetchErr == nil {
			cs.Targets = []store.Memory{*digestRow}
		}
		s.doCommitRollback(w, ctx, scope, id, cs)
	}
}

// commitUpdateRollback handles the simple updated-event rollback (restore in
// place, no tombstone) and memory.superseded with no result row.
func (s *Server) commitUpdateRollback(w http.ResponseWriter, ctx context.Context, scope identity.Scope, id string, mem *store.Memory, prior *priorStatePayload, now int64) {
	preJT, _ := s.st.Memories().GetJunctions(ctx, scope, id)
	restored := priorToMemory(prior, now)
	cs := store.CommitSet{
		Action:     store.ActionRollback,
		Memory:     restored,
		Entities:   prior.Entities,
		Keywords:   prior.Keywords,
		Queries:    prior.Queries,
		Provenance: buildProvenance(prior, scope, now),
		Events: []store.Event{{
			ID:        ulid.Make().String(),
			Type:      "memory.rolled_back",
			SubjectID: id,
			Reason:    "api: rollback updated op",
			Payload:   reconcile.MarshalPriorState(*mem, preJT),
			CreatedAt: now,
		}},
		Scope: scope,
	}
	s.doCommitRollback(w, ctx, scope, id, cs)
}

// doCommitRollback executes the commit and writes the JSON response.
func (s *Server) doCommitRollback(w http.ResponseWriter, ctx context.Context, scope identity.Scope, id string, cs store.CommitSet) {
	if err := s.st.Memories().Commit(ctx, scope, cs); err != nil {
		s.log.ErrorContext(ctx, "api: rollback commit", "id", id, "err", err)
		respondJSON(w, http.StatusInternalServerError, errBody("internal error"))
		return
	}
	s.invalidateScope(scope)
	mem2, err := s.st.Memories().Get(ctx, scope, id)
	if err != nil {
		respondJSON(w, http.StatusOK, map[string]interface{}{"id": id})
		return
	}
	respondJSON(w, http.StatusOK, memoryToJSON(mem2))
}

// parsePriorState parses a D-017 prior-state JSON payload.
// Returns an error when the payload is empty, malformed, or lacks an ID.
func parsePriorState(payload string) (*priorStatePayload, error) {
	if payload == "" || payload == "{}" {
		return nil, errNoPriorState
	}
	var p priorStatePayload
	if err := json.Unmarshal([]byte(payload), &p); err != nil {
		return nil, err
	}
	if p.ID == "" || p.Content == "" {
		return nil, errNoPriorState
	}
	return &p, nil
}

// errNoPriorState is a sentinel error for parsePriorState.
var errNoPriorState = fmt.Errorf("no prior state")

// priorToMemory converts a priorStatePayload to a store.Memory for restore.
func priorToMemory(p *priorStatePayload, updatedAt int64) store.Memory {
	return store.Memory{
		ID:          p.ID,
		Kind:        p.Kind,
		Content:     p.Content,
		Context:     p.Context,
		Status:      p.Status,
		Importance:  p.Importance,
		Confidence:  p.Confidence,
		TrustSource: p.TrustSource,
		MatchCount:  p.MatchCount,
		InjectCount: p.InjectCount,
		UseCount:    p.UseCount,
		SaveCount:   p.SaveCount,
		FailCount:   p.FailCount,
		NoiseCount:  p.NoiseCount,
		Stability:   p.Stability,
		ValidFrom:   p.ValidFrom,
		ValidUntil:  p.ValidUntil,
		EpisodeID:   p.EpisodeID,
		PrivacyZone: p.PrivacyZone,
		ContentHash: p.ContentHash,
		CreatedAt:   p.CreatedAt,
		UpdatedAt:   updatedAt,
	}
}

// buildProvenance converts provenance references in a priorStatePayload to
// store.Provenance rows for a CommitSet.
func buildProvenance(p *priorStatePayload, scope identity.Scope, now int64) []store.Provenance {
	var rows []store.Provenance
	for _, pref := range p.Provenance {
		rows = append(rows, store.Provenance{
			ID:        ulid.Make().String(),
			MemoryID:  p.ID,
			RecordID:  pref.RecordID,
			SpanStart: pref.SpanStart,
			SpanEnd:   pref.SpanEnd,
			TenantID:  scope.Tenant,
			CreatedAt: now,
		})
	}
	return rows
}

// --- PATCH /v1/memories/{id} ------------------------------------------------

// handlePatchMemory confirms or rejects a parked (pending_confirmation) memory.
// action=confirm: promotes to active, supersedes any target in memory.supersedes_id.
// action=reject:  tombstones the parked memory (status → deleted).
func (s *Server) handlePatchMemory(w http.ResponseWriter, r *http.Request) {
	if !requireJSON(w, r) {
		return
	}

	id := r.PathValue("id")
	if id == "" {
		respondJSON(w, http.StatusBadRequest, errBody("id is required"))
		return
	}

	var req patchMemoryRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		respondJSON(w, http.StatusBadRequest, errBody("decode: "+sanitizeDecodeErr(err)))
		return
	}
	if req.Action != "confirm" && req.Action != "reject" {
		respondJSON(w, http.StatusBadRequest, errBody("action must be confirm or reject"))
		return
	}

	authKey := keyFromContext(r.Context())
	scope := identity.Scope{Tenant: authKey.TenantID}
	ctx := r.Context()

	mem, err := s.st.Memories().Get(ctx, scope, id)
	if err != nil {
		if isNotFound(err) {
			respondJSON(w, http.StatusNotFound, errBody("memory not found"))
			return
		}
		s.log.ErrorContext(ctx, "api: patch memory get", "id", id, "err", err)
		respondJSON(w, http.StatusInternalServerError, errBody("internal error"))
		return
	}

	if mem.Status != "pending_confirmation" {
		respondJSON(w, http.StatusConflict, map[string]string{
			"error": "not_parked: memory is not pending confirmation",
			"code":  "not_parked",
		})
		return
	}

	now := time.Now().UnixMilli()

	switch req.Action {
	case "confirm":
		// Promote parked → active via the supersede path (D-065).
		// The target's memory.superseded event carries MarshalPriorState so the
		// promotion is itself reversible via D-064 rollback.
		promoted := *mem
		promoted.Status = "active"
		promoted.UpdatedAt = now

		var targets []store.Memory
		var targetJT store.MemoryJunctions
		if mem.SupersedesID != "" {
			target, fetchErr := s.st.Memories().Get(ctx, scope, mem.SupersedesID)
			if fetchErr == nil {
				targets = append(targets, *target)
				targetJT, _ = s.st.Memories().GetJunctions(ctx, scope, target.ID)
			} else if !isNotFound(fetchErr) {
				s.log.WarnContext(ctx, "api: confirm fetch target", "id", mem.SupersedesID, "err", fetchErr)
			}
		}

		events := []store.Event{{
			ID:        ulid.Make().String(),
			Type:      "memory.confirmed",
			SubjectID: id,
			Reason:    "api: manually confirmed",
			Payload:   "{}",
			CreatedAt: now,
		}}
		if len(targets) > 0 {
			events = append(events, store.Event{
				ID:        ulid.Make().String(),
				Type:      "memory.superseded",
				SubjectID: targets[0].ID,
				Reason:    "api: superseded on confirm (D-065)",
				// D-065: prior-state snapshot on the target makes the promotion reversible.
				Payload:   reconcile.MarshalPriorState(targets[0], targetJT),
				CreatedAt: now,
			})
		}

		cs := store.CommitSet{
			Action:  store.ActionConfirm,
			Memory:  promoted,
			Targets: targets,
			Events:  events,
			Scope:   scope,
		}
		if err := s.st.Memories().Commit(ctx, scope, cs); err != nil {
			s.log.ErrorContext(ctx, "api: confirm commit", "id", id, "err", err)
			respondJSON(w, http.StatusInternalServerError, errBody("internal error"))
			return
		}
		s.invalidateScope(scope)
		respondJSON(w, http.StatusOK, map[string]string{"id": id, "status": "active"})

	case "reject":
		// Expire the parked memory (status → 'expired', per D-065).
		rejected := *mem
		rejected.Status = "expired"
		rejected.UpdatedAt = now

		cs := store.CommitSet{
			Action: store.ActionConfirm,
			Memory: rejected,
			Events: []store.Event{{
				ID:        ulid.Make().String(),
				Type:      "memory.rejected",
				SubjectID: id,
				Reason:    "api: manually rejected",
				Payload:   "{}",
				CreatedAt: now,
			}},
			Scope: scope,
		}
		if err := s.st.Memories().Commit(ctx, scope, cs); err != nil {
			s.log.ErrorContext(ctx, "api: reject commit", "id", id, "err", err)
			respondJSON(w, http.StatusInternalServerError, errBody("internal error"))
			return
		}
		respondJSON(w, http.StatusOK, map[string]string{"id": id, "status": "expired"})
	}
}

// --- helpers ----------------------------------------------------------------

// memoryToJSON converts a *store.Memory to a memoryJSON wire type.
func memoryToJSON(m *store.Memory) memoryJSON {
	if m == nil {
		return memoryJSON{}
	}
	return memoryJSON{
		ID:             m.ID,
		Kind:           m.Kind,
		Content:        m.Content,
		Context:        m.Context,
		Status:         m.Status,
		Importance:     m.Importance,
		Confidence:     m.Confidence,
		TrustSource:    m.TrustSource,
		MatchCount:     m.MatchCount,
		InjectCount:    m.InjectCount,
		UseCount:       m.UseCount,
		SaveCount:      m.SaveCount,
		FailCount:      m.FailCount,
		NoiseCount:     m.NoiseCount,
		Stability:      m.Stability,
		ValidFrom:      m.ValidFrom,
		ValidUntil:     m.ValidUntil,
		EpisodeID:      m.EpisodeID,
		SupersedesID:   m.SupersedesID,
		SupersededByID: m.SupersededByID,
		PrivacyZone:    m.PrivacyZone,
		ContentHash:    m.ContentHash,
		CreatedAt:      m.CreatedAt,
		UpdatedAt:      m.UpdatedAt,
	}
}

// invalidateScope bumps the scope's retrieval-cache generation counter so stale
// results are not served after a rollback or confirm (D-053). No-op when no
// retriever is wired.
func (s *Server) invalidateScope(scope identity.Scope) {
	if s.retriever != nil {
		s.retriever.Cache().InvalidateScope(scope)
	}
}
