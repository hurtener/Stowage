package api

import (
	"encoding/json"
	"net/http"

	"github.com/hurtener/stowage/internal/identity"
)

// feedbackRequest is the wire format for POST /v1/feedback.
//
// Exactly one of ResponseID, MemoryID, or Citation must be set.
//
// ResponseID + signal (use|save|fail|noise): apply signal to all memories in
// the response (resolved via injection rows).
//
// MemoryID + signal (use|save|fail|noise): apply signal to one memory.
//
// Citation + signal "wrong_citation": marks the injection and bumps
// memory.noise_count + fail_count atomically (D-027 groundwork).
type feedbackRequest struct {
	ResponseID string `json:"response_id"` // response-level feedback
	MemoryID   string `json:"memory_id"`   // memory-level feedback
	Citation   string `json:"citation"`    // citation-level feedback (wrong_citation only)
	Signal     string `json:"signal"`      // use|save|fail|noise|wrong_citation
}

// feedbackResponse is the wire format for POST /v1/feedback.
type feedbackResponse struct {
	Applied int    `json:"applied"` // number of memories updated
	Signal  string `json:"signal"`
}

// validSignals maps each signal to whether it's allowed at each level.
var validMemorySignals = map[string]bool{
	"use":   true,
	"save":  true,
	"fail":  true,
	"noise": true,
}

// handleFeedback implements POST /v1/feedback.
func (s *Server) handleFeedback(w http.ResponseWriter, r *http.Request) {
	if !requireJSON(w, r) {
		return
	}

	var req feedbackRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		respondJSON(w, http.StatusBadRequest, errBody("decode: "+sanitizeDecodeErr(err)))
		return
	}

	// Validate signal.
	if req.Signal == "" {
		respondJSON(w, http.StatusBadRequest, errBody("signal must be set"))
		return
	}

	// Validate target and signal combinations.
	setCount := 0
	if req.ResponseID != "" {
		setCount++
	}
	if req.MemoryID != "" {
		setCount++
	}
	if req.Citation != "" {
		setCount++
	}
	if setCount == 0 {
		respondJSON(w, http.StatusBadRequest, errBody("one of response_id, memory_id, or citation must be set"))
		return
	}
	if setCount > 1 {
		respondJSON(w, http.StatusBadRequest, errBody("only one of response_id, memory_id, or citation may be set"))
		return
	}

	// Citation level only allows wrong_citation.
	if req.Citation != "" && req.Signal != "wrong_citation" {
		respondJSON(w, http.StatusBadRequest, errBody("citation-level feedback only accepts signal wrong_citation"))
		return
	}
	// Response/memory level: only use|save|fail|noise.
	if req.Citation == "" && !validMemorySignals[req.Signal] {
		respondJSON(w, http.StatusBadRequest, errBody("signal must be one of use|save|fail|noise"))
		return
	}

	authKey := keyFromContext(r.Context())
	scope := identity.Scope{Tenant: authKey.TenantID}

	switch {
	case req.Citation != "":
		// Citation level: MarkWrongCitation atomically updates injection + memory.
		if err := s.st.Injections().MarkWrongCitation(r.Context(), scope, req.Citation); err != nil {
			if isNotFound(err) {
				respondJSON(w, http.StatusNotFound, errBody("citation not found"))
				return
			}
			s.log.ErrorContext(r.Context(), "api: feedback wrong_citation", "err", err)
			respondJSON(w, http.StatusInternalServerError, errBody("internal error"))
			return
		}
		respondJSON(w, http.StatusOK, feedbackResponse{Applied: 1, Signal: req.Signal})

	case req.MemoryID != "":
		// Memory level: apply signal to one memory.
		if err := s.st.Memories().ApplyFeedback(r.Context(), scope, req.MemoryID, req.Signal); err != nil {
			s.log.ErrorContext(r.Context(), "api: feedback memory", "err", err)
			respondJSON(w, http.StatusInternalServerError, errBody("internal error"))
			return
		}
		respondJSON(w, http.StatusOK, feedbackResponse{Applied: 1, Signal: req.Signal})

	case req.ResponseID != "":
		// Response level: resolve injections, apply signal to each memory.
		injections, err := s.st.Injections().ListByResponse(r.Context(), scope, req.ResponseID)
		if err != nil {
			s.log.ErrorContext(r.Context(), "api: feedback list injections", "err", err)
			respondJSON(w, http.StatusInternalServerError, errBody("internal error"))
			return
		}

		// Deduplicate memory IDs (same memory may appear in multiple injections).
		seen := make(map[string]bool, len(injections))
		applied := 0
		for _, inj := range injections {
			if seen[inj.MemoryID] {
				continue
			}
			seen[inj.MemoryID] = true
			if err := s.st.Memories().ApplyFeedback(r.Context(), scope, inj.MemoryID, req.Signal); err != nil {
				s.log.WarnContext(r.Context(), "api: feedback response apply", "memory_id", inj.MemoryID, "err", err)
				// Non-fatal: continue applying to remaining memories.
				continue
			}
			applied++
		}
		respondJSON(w, http.StatusOK, feedbackResponse{Applied: applied, Signal: req.Signal})

	default:
		// Should never reach here due to validation above.
		respondJSON(w, http.StatusBadRequest, errBody("invalid feedback request"))
	}

}
