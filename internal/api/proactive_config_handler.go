package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/proactive"
)

// proactiveConfigJSON is the GET/PUT /v1/admin/proactive envelope — the scope's
// effective proactive governance (RFC §6d, D-087). GET returns the resolved config
// (profile default ⊕ stored override); PUT stores the scope's override.
type proactiveConfigJSON struct {
	Enabled   bool            `json:"enabled"`
	Threshold float64         `json:"threshold"`
	Budget    int             `json:"budget"`
	Classes   map[string]bool `json:"classes"`
}

// handleGetProactiveConfig implements GET /v1/admin/proactive (admin tier). It
// returns the effective governance for the scope (?user=&project= refine it).
func (s *Server) handleGetProactiveConfig(w http.ResponseWriter, r *http.Request) {
	authKey := keyFromContext(r.Context())
	q := r.URL.Query()
	scope := identity.Scope{Tenant: authKey.TenantID, User: q.Get("user"), Project: q.Get("project")}

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

	var body proactiveConfigJSON
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respondJSON(w, http.StatusBadRequest, errBody("invalid request body"))
		return
	}
	cfg := proactive.Config{Enabled: body.Enabled, Threshold: body.Threshold, Budget: body.Budget, Classes: body.Classes}
	value, err := proactive.MarshalConfig(cfg)
	if err != nil {
		s.log.ErrorContext(r.Context(), "api: proactive config: marshal failed", "err", err)
		respondJSON(w, http.StatusInternalServerError, errBody("proactive config marshal failed"))
		return
	}
	if err := s.st.ScopeSettings().Set(r.Context(), scope, "proactive", value, time.Now().UnixMilli()); err != nil {
		s.log.ErrorContext(r.Context(), "api: proactive config: store failed", "err", err)
		respondJSON(w, http.StatusInternalServerError, errBody("proactive config store failed"))
		return
	}
	// Echo the canonical (clamped) config exactly as stored — `value` is the
	// marshaled clamped form, so decoding it back is the resolved config without a
	// second store round-trip.
	var stored proactiveConfigJSON
	_ = json.Unmarshal([]byte(value), &stored)
	if stored.Classes == nil {
		stored.Classes = map[string]bool{}
	}
	respondJSON(w, http.StatusOK, stored)
}

func proactiveConfigToJSON(cfg proactive.Config) proactiveConfigJSON {
	classes := cfg.Classes
	if classes == nil {
		classes = map[string]bool{}
	}
	return proactiveConfigJSON{Enabled: cfg.Enabled, Threshold: cfg.Threshold, Budget: cfg.Budget, Classes: classes}
}
