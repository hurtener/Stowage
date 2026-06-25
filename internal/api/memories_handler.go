package api

// Phase 18 — rollback & pending-confirmation resolution (D-064, D-065).
// Phase h3 — re-homed onto the exported internal/reconcile reversibility core
// (D-070): these handlers are THIN CALLERS. All rollback/confirm orchestration
// (newest-event inverse, merge all-or-nothing, the 409 conflict guards) lives in
// reconcile.Rollback / reconcile.Resolve / reconcile.GetMemory; the handlers map
// the typed errors onto HTTP status codes. Behavior is preserved byte-for-byte
// (the Phase 18 api tests pass unmodified).
//
// Endpoints:
//   GET  /v1/memories/{id}          — read memory + junctions + ancestor chain
//   POST /v1/memories/{id}/rollback — restore from prior-state event payload
//   PATCH /v1/memories/{id}         — confirm or reject a parked memory

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

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

// provRefJSON is a compact provenance reference.
type provRefJSON struct {
	RecordID  string `json:"record_id"`
	SpanStart int    `json:"span_start,omitempty"`
	SpanEnd   int    `json:"span_end,omitempty"`
}

// patchMemoryRequest is the wire format for PATCH /v1/memories/{id}.
type patchMemoryRequest struct {
	Action string `json:"action"` // "confirm" | "reject"
	// ProjectID/UserID scope the confirm/reject to a sub-tenant identity (P3, D-125);
	// empty = tenant-wide. Prevents a caller resolving another user's parked memory.
	ProjectID string `json:"project_id"`
	UserID    string `json:"user_id"`
}

// --- GET /v1/memories/{id} --------------------------------------------------

// handleGetMemory returns the memory, its junction rows, and up to 10 ancestors
// in the supersedes chain. Thin caller over reconcile.GetMemory (D-070).
func (s *Server) handleGetMemory(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		respondJSON(w, http.StatusBadRequest, errBody("id is required"))
		return
	}

	scope := scopeFromRequest(r)
	ctx := r.Context()

	view, err := reconcile.GetMemory(ctx, s.st, scope, id)
	if err != nil {
		s.respondReconcileErr(w, ctx, "get memory", id, err)
		return
	}

	mem := view.Memory
	resp := memoryResponse{
		Memory:          memoryToJSON(&mem),
		Entities:        view.Entities,
		Keywords:        view.Keywords,
		Queries:         view.Queries,
		SupersedesChain: view.SupersedesChain,
	}
	for _, p := range view.Provenance {
		resp.Provenance = append(resp.Provenance, provRefJSON{
			RecordID:  p.RecordID,
			SpanStart: p.SpanStart,
			SpanEnd:   p.SpanEnd,
		})
	}

	respondJSON(w, http.StatusOK, resp)
}

// --- POST /v1/memories/{id}/rollback ----------------------------------------

// handleRollbackMemory restores a memory to the state recorded in the most
// recent D-017 prior-state event. Thin caller over reconcile.Rollback (D-064/D-070).
// Conflict guards (409): already_rolled_back, invalid_prior_state,
// downstream_conflict, incomplete_snapshots, no_prior_state.
func (s *Server) handleRollbackMemory(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		respondJSON(w, http.StatusBadRequest, errBody("id is required"))
		return
	}

	scope := scopeFromRequest(r)
	ctx := r.Context()

	res, err := reconcile.Rollback(ctx, s.st, scope, id, s.scopeInvalidator())
	if err != nil {
		s.respondReconcileErr(w, ctx, "rollback", id, err)
		return
	}

	// Cache invalidation now happens inside reconcile.Rollback (D-053, Wave-B
	// checkpoint) — passing s.scopeInvalidator() above is the single invalidation.
	if res.Memory != nil {
		respondJSON(w, http.StatusOK, memoryToJSON(res.Memory))
		return
	}
	respondJSON(w, http.StatusOK, map[string]interface{}{"id": res.ID})
}

// --- PATCH /v1/memories/{id} ------------------------------------------------

// handlePatchMemory confirms or rejects a parked (pending_confirmation) memory.
// Thin caller over reconcile.Resolve (D-065/D-070).
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
	scope := identity.Scope{Tenant: authKey.TenantID, Project: req.ProjectID, User: req.UserID}
	ctx := r.Context()

	res, err := reconcile.Resolve(ctx, s.st, scope, id, reconcile.ConfirmAction(req.Action), s.scopeInvalidator())
	if err != nil {
		s.respondReconcileErr(w, ctx, "patch memory", id, err)
		return
	}

	// Cache invalidation (on confirm only, per res.Invalidate) now happens inside
	// reconcile.Resolve (D-053, Wave-B checkpoint) — the single invalidation.
	respondJSON(w, http.StatusOK, map[string]string{"id": res.ID, "status": res.Status})
}

// --- error mapping ----------------------------------------------------------

// respondReconcileErr maps a reconcile-core error onto an HTTP status: 404 for a
// not-found memory, 409 (with the stable code) for a typed *ConflictError, and
// 500 for anything else (logged). Behavior-preserving: the codes and statuses
// match the pre-h3 Phase 18 handlers.
func (s *Server) respondReconcileErr(w http.ResponseWriter, ctx context.Context, op, id string, err error) {
	if isNotFound(err) {
		respondJSON(w, http.StatusNotFound, errBody("memory not found"))
		return
	}
	var ce *reconcile.ConflictError
	if errors.As(err, &ce) {
		respondJSON(w, http.StatusConflict, map[string]string{
			"error": ce.Msg,
			"code":  ce.Code,
		})
		return
	}
	s.log.ErrorContext(ctx, "api: "+op, "id", id, "err", err)
	respondJSON(w, http.StatusInternalServerError, errBody("internal error"))
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

// scopeInvalidator returns the retrieval-cache invalidator the reconcile core
// uses to bust stale results after a content-changing commit (D-053). It returns
// an untyped-nil interface when no retriever is wired, so the core's nil check is
// safe (a typed-nil *ResultCache would panic on use).
func (s *Server) scopeInvalidator() reconcile.ScopeInvalidator {
	if s.retriever != nil {
		return s.retriever.Cache()
	}
	return nil
}
