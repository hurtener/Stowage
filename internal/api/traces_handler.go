package api

import (
	"net/http"
	"time"

	"github.com/hurtener/stowage/internal/traces"
)

// handleTrace implements GET /v1/traces/{response_id} (RFC §6c, D-086): the read-only,
// deterministic reasoning-trace export — the memory-into-conclusion chain reconstructed
// from the day-one tables, returned as an optionally ed25519-signed bundle. An unknown
// response_id yields an empty trace (200, not 404) — `response_id` is a filter, not a
// REST resource (parity with the other reads). Scope from the auth key (P3).
func (s *Server) handleTrace(w http.ResponseWriter, r *http.Request) {
	scope, err := s.scopeFromRequest(r)
	if err != nil {
		respondScopeError(w, err)
		return
	}
	// The mux pattern {response_id} guarantees a non-empty segment; an empty/unknown
	// id is handled by Reconstruct returning an empty trace (200, not an error).
	responseID := r.PathValue("response_id")
	tr, err := traces.Reconstruct(r.Context(), s.st, scope, responseID, time.Now().UnixMilli())
	if err != nil {
		s.log.ErrorContext(r.Context(), "api: trace: reconstruct failed", "err", err)
		respondJSON(w, http.StatusInternalServerError, errBody("trace reconstruct failed"))
		return
	}
	bundle, err := traces.Sign(tr, s.traceSigner)
	if err != nil {
		s.log.ErrorContext(r.Context(), "api: trace: sign failed", "err", err)
		respondJSON(w, http.StatusInternalServerError, errBody("trace sign failed"))
		return
	}
	respondJSON(w, http.StatusOK, bundle)
}
