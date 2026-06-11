package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/store"
)

type branchRequest struct {
	Action         string `json:"action"`           // "fork"|"merge"|"discard"
	SessionID      string `json:"session_id"`       // required for fork
	BranchID       string `json:"branch_id"`        // required for merge/discard
	ParentBranchID string `json:"parent_branch_id"` // optional for fork
}

type forkResponse struct {
	BranchID string `json:"branch_id"`
}

// handleBranches implements POST /v1/branches.
//
// Actions:
//   - fork:    Create a new open branch for a session. Returns {branch_id}.
//   - merge:   Transition branch status to "merged". Returns {}.
//   - discard: Transition branch status to "discarded". Records remain
//     readable (P1 fidelity). Returns {}.
func (s *Server) handleBranches(w http.ResponseWriter, r *http.Request) {
	if !requireJSON(w, r) {
		return
	}

	var req branchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondJSON(w, http.StatusBadRequest, errBody("decode: "+err.Error()))
		return
	}

	authKey := keyFromContext(r.Context())
	scope := identity.Scope{Tenant: authKey.TenantID}

	switch req.Action {
	case "fork":
		if req.SessionID == "" {
			respondJSON(w, http.StatusBadRequest, errBody("session_id is required for fork"))
			return
		}
		now := time.Now().UnixMilli()
		br := store.Branch{
			ID:             ulid.Make().String(),
			SessionID:      req.SessionID,
			ParentBranchID: req.ParentBranchID,
			Status:         "open",
			CreatedAt:      now,
			UpdatedAt:      now,
		}
		if err := s.st.Branches().Create(r.Context(), scope, br); err != nil {
			s.log.ErrorContext(r.Context(), "api: branches: fork failed", "err", err)
			respondJSON(w, http.StatusInternalServerError, errBody("store error"))
			return
		}
		respondJSON(w, http.StatusCreated, forkResponse{BranchID: br.ID})

	case "merge":
		if req.BranchID == "" {
			respondJSON(w, http.StatusBadRequest, errBody("branch_id is required for merge"))
			return
		}
		if err := s.st.Branches().SetStatus(r.Context(), scope, req.BranchID, "merged", time.Now().UnixMilli()); err != nil {
			if isNotFound(err) {
				respondJSON(w, http.StatusNotFound, errBody("branch not found"))
				return
			}
			s.log.ErrorContext(r.Context(), "api: branches: merge failed", "err", err)
			respondJSON(w, http.StatusInternalServerError, errBody("store error"))
			return
		}
		respondJSON(w, http.StatusOK, struct{}{})

	case "discard":
		if req.BranchID == "" {
			respondJSON(w, http.StatusBadRequest, errBody("branch_id is required for discard"))
			return
		}
		if err := s.st.Branches().SetStatus(r.Context(), scope, req.BranchID, "discarded", time.Now().UnixMilli()); err != nil {
			if isNotFound(err) {
				respondJSON(w, http.StatusNotFound, errBody("branch not found"))
				return
			}
			s.log.ErrorContext(r.Context(), "api: branches: discard failed", "err", err)
			respondJSON(w, http.StatusInternalServerError, errBody("store error"))
			return
		}
		// Phase 06: flush any buffers associated with this branch (fire-and-forget).
		if s.stage != nil {
			go s.stage.FlushBranch(r.Context(), req.BranchID)
		}
		respondJSON(w, http.StatusOK, struct{}{})

	default:
		respondJSON(w, http.StatusBadRequest,
			errBody("action must be fork|merge|discard"))
	}
}

// isNotFound reports whether err wraps store.ErrNotFound.
func isNotFound(err error) bool {
	return errors.Is(err, store.ErrNotFound)
}
