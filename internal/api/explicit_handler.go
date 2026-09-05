package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/hurtener/stowage/internal/reconcile"
	"github.com/hurtener/stowage/internal/store"
)

type explicitHTTPRequest struct {
	reconcile.ExplicitRequest
	ProjectID string `json:"project_id,omitempty"`
	UserID    string `json:"user_id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
}

func (s *Server) handleRemember(w http.ResponseWriter, r *http.Request) {
	s.handleExplicit(w, r, false)
}
func (s *Server) handleCorrect(w http.ResponseWriter, r *http.Request) { s.handleExplicit(w, r, true) }

func (s *Server) handleExplicit(w http.ResponseWriter, r *http.Request, correction bool) {
	if !requireJSON(w, r) {
		return
	}
	var in explicitHTTPRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		respondJSON(w, http.StatusBadRequest, errBody("invalid memory command JSON"))
		return
	}
	var trailing any
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		respondJSON(w, http.StatusBadRequest, errBody("expected one JSON object"))
		return
	}
	if key := r.Header.Get("Idempotency-Key"); key != "" {
		if in.IdempotencyKey != "" && in.IdempotencyKey != key {
			respondJSON(w, http.StatusConflict, errBody("idempotency header conflicts with body"))
			return
		}
		in.IdempotencyKey = key
	}
	scope, session, err := s.resolveScope(r, identityArgs{Project: in.ProjectID, User: in.UserID, Session: in.SessionID})
	if err != nil {
		respondScopeError(w, err)
		return
	}
	scope.Session = session
	var inv reconcile.ScopeInvalidator
	if s.retriever != nil {
		inv = s.retriever.Cache()
	}
	var receipt *reconcile.ExplicitReceipt
	if correction {
		receipt, err = reconcile.Correct(r.Context(), s.st, scope, in.ExplicitRequest, inv)
	} else {
		receipt, err = reconcile.Remember(r.Context(), s.st, scope, in.ExplicitRequest, inv)
	}
	if err != nil {
		switch {
		case errors.Is(err, reconcile.ErrInvalidCommand), errors.Is(err, reconcile.ErrInvalidEvidence):
			respondJSON(w, http.StatusBadRequest, errBody(err.Error()))
		case errors.Is(err, reconcile.ErrIdempotencyConflict), errors.Is(err, store.ErrCommandConflict), errors.Is(err, store.ErrDuplicateContent):
			respondJSON(w, http.StatusConflict, errBody("memory command conflict; inspect the current state or retry the identical request"))
		case errors.Is(err, store.ErrNotFound):
			respondJSON(w, http.StatusNotFound, errBody("memory not found"))
		default:
			s.log.ErrorContext(r.Context(), "explicit memory command failed", "err", err)
			respondJSON(w, http.StatusInternalServerError, errBody("memory command failed; retry with the same idempotency key"))
		}
		return
	}
	respondJSON(w, http.StatusOK, receipt)
}
