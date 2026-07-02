package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/hurtener/stowage/internal/pipeline"
	"github.com/hurtener/stowage/internal/store"
)

type branchRequest struct {
	Action         string `json:"action"`           // "fork"|"merge"|"discard"
	SessionID      string `json:"session_id"`       // required for fork
	BranchID       string `json:"branch_id"`        // required for merge/discard
	ParentBranchID string `json:"parent_branch_id"` // optional for fork
	// ProjectID/UserID scope the branch mutate to a sub-tenant identity (P3, D-125);
	// empty = tenant-wide. Prevents forking/merging/discarding another user's branch.
	ProjectID string `json:"project_id"`
	UserID    string `json:"user_id"`
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

	scope, effSession, err := s.resolveScope(r, identityArgs{Project: req.ProjectID, User: req.UserID, Session: req.SessionID})
	if err != nil {
		respondScopeError(w, err)
		return
	}

	// All three actions route through the shared pipeline branch core (D-071) so
	// the HTTP, MCP, and SDK surfaces cannot drift; discard sets SkipPromotion via
	// the branch-discard flush trigger (D-029).
	switch req.Action {
	case "fork":
		if req.SessionID == "" {
			respondJSON(w, http.StatusBadRequest, errBody("session_id is required for fork"))
			return
		}
		// Session-REPLACE (D-137/D-150): the effective session (claim > arg — HTTP
		// has no _meta, D-140), never Scope.Session.
		id, err := pipeline.ForkBranch(r.Context(), s.st, scope, effSession, req.ParentBranchID)
		if err != nil {
			s.log.ErrorContext(r.Context(), "api: branches: fork failed", "err", err)
			respondJSON(w, http.StatusInternalServerError, errBody("store error"))
			return
		}
		respondJSON(w, http.StatusCreated, forkResponse{BranchID: id})

	case "merge":
		if req.BranchID == "" {
			respondJSON(w, http.StatusBadRequest, errBody("branch_id is required for merge"))
			return
		}
		if err := pipeline.MergeBranch(r.Context(), s.st, scope, req.BranchID); err != nil {
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
		if err := pipeline.DiscardBranch(r.Context(), s.st, s.stage, scope, req.BranchID); err != nil {
			if isNotFound(err) {
				respondJSON(w, http.StatusNotFound, errBody("branch not found"))
				return
			}
			s.log.ErrorContext(r.Context(), "api: branches: discard failed", "err", err)
			respondJSON(w, http.StatusInternalServerError, errBody("store error"))
			return
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
