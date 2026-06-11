package api

import (
	"net/http"
)

// handleHealthz returns 200 when the process is alive.
// It performs no I/O and never blocks.
func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	respondJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleReadyz returns 200 when the store is reachable (store ping via Migrate
// idempotency), 503 when it is not. Used by load-balancers and liveness probes.
func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	// Ping the store by running a no-op query: ListUnprocessed with limit 0.
	// This exercises the read path without mutating any state.
	_, err := s.st.Records().ListUnprocessed(r.Context(), 0, 0)
	if err != nil {
		s.log.ErrorContext(r.Context(), "api: readyz: store unreachable", "err", err)
		respondJSON(w, http.StatusServiceUnavailable, map[string]string{
			"status": "unhealthy",
			"error":  "store unreachable",
		})
		return
	}
	respondJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
