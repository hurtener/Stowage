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
	"net/http"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/hurtener/stowage/internal/identity"
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
// recent D-017 prior-state event payload. Three conflict guards return 409:
//  1. Memory is already deleted (status == 'deleted').
//  2. No prior-state event found in the event log (limit 20).
//  3. Prior-state payload is malformed or has no content.
func (s *Server) handleRollbackMemory(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		respondJSON(w, http.StatusBadRequest, errBody("id is required"))
		return
	}

	authKey := keyFromContext(r.Context())
	scope := identity.Scope{Tenant: authKey.TenantID}
	ctx := r.Context()

	// Fetch current memory.
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

	// Guard 1: already deleted.
	if mem.Status == "deleted" {
		respondJSON(w, http.StatusConflict, map[string]string{
			"error": "already_deleted: memory is already deleted; cannot roll back",
			"code":  "already_deleted",
		})
		return
	}

	// Guard 2: find the most recent event with a valid prior-state payload.
	events, err := s.st.Events().ListBySubject(ctx, scope, id, 20)
	if err != nil {
		s.log.ErrorContext(ctx, "api: rollback list events", "id", id, "err", err)
		respondJSON(w, http.StatusInternalServerError, errBody("internal error"))
		return
	}

	var prior *priorStatePayload
	for _, ev := range events {
		if ev.Payload == "" || ev.Payload == "{}" {
			continue
		}
		var p priorStatePayload
		if err := json.Unmarshal([]byte(ev.Payload), &p); err != nil {
			continue
		}
		if p.ID == "" || p.Content == "" {
			continue // not a prior-state payload
		}
		prior = &p
		break
	}

	if prior == nil {
		respondJSON(w, http.StatusConflict, map[string]string{
			"error": "no_prior_state: no restorable prior state found in event log",
			"code":  "no_prior_state",
		})
		return
	}

	// Guard 3: validate prior-state content (already checked above via p.Content == "").
	// Redundant but explicit.
	if prior.Content == "" {
		respondJSON(w, http.StatusConflict, map[string]string{
			"error": "invalid_prior_state: prior-state payload has no content",
			"code":  "invalid_prior_state",
		})
		return
	}

	// Build the restored memory from the prior-state payload.
	now := time.Now().UnixMilli()
	restored := store.Memory{
		ID:          prior.ID,
		Kind:        prior.Kind,
		Content:     prior.Content,
		Context:     prior.Context,
		Status:      prior.Status,
		Importance:  prior.Importance,
		Confidence:  prior.Confidence,
		TrustSource: prior.TrustSource,
		MatchCount:  prior.MatchCount,
		InjectCount: prior.InjectCount,
		UseCount:    prior.UseCount,
		SaveCount:   prior.SaveCount,
		FailCount:   prior.FailCount,
		NoiseCount:  prior.NoiseCount,
		Stability:   prior.Stability,
		ValidFrom:   prior.ValidFrom,
		ValidUntil:  prior.ValidUntil,
		EpisodeID:   prior.EpisodeID,
		PrivacyZone: prior.PrivacyZone,
		ContentHash: prior.ContentHash,
		CreatedAt:   prior.CreatedAt,
		UpdatedAt:   now,
	}

	// Build provenance rows from prior-state payload.
	var storeProvenance []store.Provenance
	for _, p := range prior.Provenance {
		storeProvenance = append(storeProvenance, store.Provenance{
			ID:        ulid.Make().String(),
			MemoryID:  restored.ID,
			RecordID:  p.RecordID,
			SpanStart: p.SpanStart,
			SpanEnd:   p.SpanEnd,
			TenantID:  scope.Tenant,
			CreatedAt: now,
		})
	}

	// Determine targets: if the current memory has a superseder, tombstone it.
	var targets []store.Memory
	if mem.SupersededByID != "" {
		superseder, err := s.st.Memories().Get(ctx, scope, mem.SupersededByID)
		if err == nil {
			targets = append(targets, *superseder)
		} else if !isNotFound(err) {
			s.log.WarnContext(ctx, "api: rollback fetch superseder", "id", mem.SupersededByID, "err", err)
			// Non-fatal: proceed without tombstoning.
		}
	}

	cs := store.CommitSet{
		Action:     store.ActionRollback,
		Memory:     restored,
		Entities:   prior.Entities,
		Keywords:   prior.Keywords,
		Queries:    prior.Queries,
		Provenance: storeProvenance,
		Targets:    targets,
		Events: []store.Event{{
			ID:        ulid.Make().String(),
			Type:      "memory.rolled_back",
			SubjectID: id,
			Reason:    "api: manual rollback requested",
			Payload:   "{}",
			CreatedAt: now,
		}},
		Scope: scope,
	}

	if err := s.st.Memories().Commit(ctx, scope, cs); err != nil {
		s.log.ErrorContext(ctx, "api: rollback commit", "id", id, "err", err)
		respondJSON(w, http.StatusInternalServerError, errBody("internal error"))
		return
	}

	// Invalidate the scope cache so the restored content is immediately retrievable.
	s.invalidateScope(scope)

	// Return the restored memory state.
	mem2, err := s.st.Memories().Get(ctx, scope, id)
	if err != nil {
		// Commit succeeded; return minimal response.
		respondJSON(w, http.StatusOK, map[string]interface{}{
			"id":     id,
			"status": restored.Status,
		})
		return
	}
	respondJSON(w, http.StatusOK, memoryToJSON(mem2))
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
		// Promote parked → active; supersede any target listed in supersedes_id.
		promoted := *mem
		promoted.Status = "active"
		promoted.UpdatedAt = now

		var targets []store.Memory
		if mem.SupersedesID != "" {
			target, err := s.st.Memories().Get(ctx, scope, mem.SupersedesID)
			if err == nil {
				targets = append(targets, *target)
			} else if !isNotFound(err) {
				s.log.WarnContext(ctx, "api: confirm fetch target", "id", mem.SupersedesID, "err", err)
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
				Reason:    "api: superseded on confirm",
				Payload:   "{}",
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
		// Tombstone the parked memory using ActionConfirm with status=deleted.
		rejected := *mem
		rejected.Status = "deleted"
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
		respondJSON(w, http.StatusOK, map[string]string{"id": id, "status": "deleted"})
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
