package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/reconcile"
	"github.com/hurtener/stowage/internal/store"
	"github.com/hurtener/stowage/internal/trust"
)

// reviewItemJSON is one pending_review memory in the queue (mirrors SDK + MCP).
type reviewItemJSON struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Content   string `json:"content"`
	Context   string `json:"context,omitempty"`
	CreatedAt int64  `json:"created_at"`
}

type reviewListResponseJSON struct {
	// omitempty for cross-surface wire parity (D-067): the MCP + SDK ReviewOutput
	// also omit an empty items list, so an empty queue is byte-identical everywhere.
	Items      []reviewItemJSON `json:"items,omitempty"`
	NextCursor string           `json:"next_cursor,omitempty"`
}

type reviewResolveRequestJSON struct {
	Action string `json:"action"` // approve | reject
}

type reviewResolveResponseJSON struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

// handleReviewList implements GET /v1/review (RFC §6c, D-084): list the scope's
// pending_review memories.
func (s *Server) handleReviewList(w http.ResponseWriter, r *http.Request) {
	authKey := keyFromContext(r.Context())
	scope := identity.Scope{Tenant: authKey.TenantID}
	q := r.URL.Query()

	mems, next, err := trust.ListPending(r.Context(), s.st, scope, atoiDefault(q.Get("limit"), 0), q.Get("cursor"))
	if err != nil {
		s.log.ErrorContext(r.Context(), "api: review list failed", "err", err)
		respondJSON(w, http.StatusInternalServerError, errBody("review list failed"))
		return
	}
	out := reviewListResponseJSON{Items: make([]reviewItemJSON, 0, len(mems)), NextCursor: next}
	for _, m := range mems {
		out.Items = append(out.Items, reviewItemJSON{ID: m.ID, Kind: m.Kind, Content: m.Content, Context: m.Context, CreatedAt: m.CreatedAt})
	}
	respondJSON(w, http.StatusOK, out)
}

// handleReviewResolve implements POST /v1/review/{id} (RFC §6c, D-084): approve
// (→active) or reject (→quarantined) a pending_review memory.
func (s *Server) handleReviewResolve(w http.ResponseWriter, r *http.Request) {
	if !requireJSON(w, r) {
		return
	}
	authKey := keyFromContext(r.Context())
	scope := identity.Scope{Tenant: authKey.TenantID}
	id := r.PathValue("id")
	if id == "" {
		respondJSON(w, http.StatusBadRequest, errBody("memory id required"))
		return
	}
	var req reviewResolveRequestJSON
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		respondJSON(w, http.StatusBadRequest, errBody("decode: "+sanitizeDecodeErr(err)))
		return
	}
	if req.Action != string(trust.ReviewApprove) && req.Action != string(trust.ReviewReject) {
		respondJSON(w, http.StatusBadRequest, errBody("action must be approve or reject"))
		return
	}
	res, err := trust.Resolve(r.Context(), s.st, scope, id, trust.ReviewAction(req.Action), s.reviewInvalidator()...)
	if errors.Is(err, trust.ErrNotPending) {
		respondJSON(w, http.StatusConflict, errBody("memory is not pending_review"))
		return
	}
	if errors.Is(err, store.ErrNotFound) {
		respondJSON(w, http.StatusNotFound, errBody("memory not found"))
		return
	}
	if err != nil {
		s.log.ErrorContext(r.Context(), "api: review resolve failed", "err", err)
		respondJSON(w, http.StatusInternalServerError, errBody("review resolve failed"))
		return
	}
	respondJSON(w, http.StatusOK, reviewResolveResponseJSON{ID: res.ID, Status: res.Status})
}

// reviewInvalidator returns the retrieval-cache invalidator (so an approved memory's
// content busts stale cached results), or nothing when no retriever is wired.
func (s *Server) reviewInvalidator() []reconcile.ScopeInvalidator {
	if s.retriever != nil {
		return []reconcile.ScopeInvalidator{s.retriever.Cache()}
	}
	return nil
}
