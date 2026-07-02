package api

import (
	"encoding/json"
	"net/http"

	"github.com/hurtener/stowage/internal/trust"
)

// verifyRequestJSON is the POST /v1/verify body (mirrors SDK + MCP).
type verifyRequestJSON struct {
	Claim     string   `json:"claim"`
	Citations []string `json:"citations,omitempty"`
	// ProjectID/UserID scope the citation/memory reads to a sub-tenant identity (P3,
	// D-125); empty = tenant-wide. A claim verifies only against the scope's memories.
	ProjectID string `json:"project_id"`
	UserID    string `json:"user_id"`
}

// verifyResponseJSON is the verification verdict envelope.
type verifyResponseJSON struct {
	Verdict     string  `json:"verdict"`
	Confidence  float64 `json:"confidence"`
	Explanation string  `json:"explanation,omitempty"`
	Degraded    bool    `json:"degraded,omitempty"`
}

// handleVerify implements POST /v1/verify (RFC §6c, D-084): resolve the claim's
// citation handles and run a schema-constrained gateway entailment check. Degrades to
// unclear (200) when the gateway is unreachable (D-036).
func (s *Server) handleVerify(w http.ResponseWriter, r *http.Request) {
	if !requireJSON(w, r) {
		return
	}

	var req verifyRequestJSON
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		respondJSON(w, http.StatusBadRequest, errBody("decode: "+sanitizeDecodeErr(err)))
		return
	}
	if req.Claim == "" {
		respondJSON(w, http.StatusBadRequest, errBody("claim must be set"))
		return
	}
	scope, _, err := s.resolveScope(r, identityArgs{Project: req.ProjectID, User: req.UserID})
	if err != nil {
		respondScopeError(w, err)
		return
	}
	v, err := trust.VerifyClaim(r.Context(), s.st, s.gw, scope, req.Claim, req.Citations)
	if err != nil {
		s.log.ErrorContext(r.Context(), "api: verify failed", "err", err)
		respondJSON(w, http.StatusInternalServerError, errBody("verify failed"))
		return
	}
	respondJSON(w, http.StatusOK, verifyResponseJSON{
		Verdict: v.Verdict, Confidence: v.Confidence, Explanation: v.Explanation, Degraded: v.Degraded,
	})
}
