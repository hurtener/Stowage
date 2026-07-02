package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/proactive"
)

// proactiveConfigJSON is the GET /v1/admin/proactive response — the scope's
// effective proactive governance (RFC §6d, D-087).
type proactiveConfigJSON struct {
	Enabled   bool            `json:"enabled"`
	Threshold float64         `json:"threshold"`
	Budget    int             `json:"budget"`
	Classes   map[string]bool `json:"classes"`
}

// proactiveConfigPatchJSON is the PUT /v1/admin/proactive body. Every field is
// optional (pointer); an omitted field is left unchanged so a partial update never
// zero-wipes the rest of the config. Opt-out is {"enabled": false}.
type proactiveConfigPatchJSON struct {
	Enabled   *bool           `json:"enabled,omitempty"`
	Threshold *float64        `json:"threshold,omitempty"`
	Budget    *int            `json:"budget,omitempty"`
	Classes   map[string]bool `json:"classes,omitempty"`
}

// handleGetProactiveConfig implements GET /v1/admin/proactive (admin tier). It
// returns the effective governance for the scope (?user=&project= refine it).
func (s *Server) handleGetProactiveConfig(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	scope, _, err := s.resolveScope(r, identityArgs{User: q.Get("user"), Project: q.Get("project")})
	if err != nil {
		respondScopeError(w, err)
		return
	}

	cfg, err := proactive.Resolve(r.Context(), s.st.ScopeSettings(), scope, proactiveDefault(s.profile))
	if err != nil {
		s.log.ErrorContext(r.Context(), "api: proactive config: resolve failed", "err", err)
		respondJSON(w, http.StatusInternalServerError, errBody("proactive config resolve failed"))
		return
	}
	respondJSON(w, http.StatusOK, proactiveConfigToJSON(cfg))
}

// handlePutProactiveConfig implements PUT /v1/admin/proactive (admin tier). It
// writes the scope's stored "proactive" governance override (canonicalised +
// clamped via proactive.MarshalConfig). Opt-out is {enabled:false}.
func (s *Server) handlePutProactiveConfig(w http.ResponseWriter, r *http.Request) {
	authKey := keyFromContext(r.Context())
	q := r.URL.Query()
	scope := identity.Scope{Tenant: authKey.TenantID, User: q.Get("user"), Project: q.Get("project")}

	var body proactiveConfigPatchJSON
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respondJSON(w, http.StatusBadRequest, errBody("invalid request body"))
		return
	}
	patch := proactive.ConfigPatch{Enabled: body.Enabled, Threshold: body.Threshold, Budget: body.Budget, Classes: body.Classes}
	stored, err := proactive.WriteGovernance(r.Context(), s.st.ScopeSettings(), scope, proactiveDefault(s.profile), patch, time.Now().UnixMilli())
	if err != nil {
		s.log.ErrorContext(r.Context(), "api: proactive config: write failed", "err", err)
		respondJSON(w, http.StatusInternalServerError, errBody("proactive config write failed"))
		return
	}
	respondJSON(w, http.StatusOK, proactiveConfigToJSON(stored))
}

func proactiveConfigToJSON(cfg proactive.Config) proactiveConfigJSON {
	classes := cfg.Classes
	if classes == nil {
		classes = map[string]bool{}
	}
	return proactiveConfigJSON{Enabled: cfg.Enabled, Threshold: cfg.Threshold, Budget: cfg.Budget, Classes: classes}
}
