package api

// agentpolicy_handler.go — HTTP handlers for the read-time agent->topic policy
// binding (Phase ae1, D-135/D-146/D-151). Mirrors grants_handler.go's shape.
//
// Routes (registered in server.go):
//
//	Scope-aware (agent or admin key):
//	  GET    /v1/scopes/agent-policies             — list the tenant's agent bindings
//	  PUT    /v1/scopes/agent-policies              — upsert a binding (atomic replace)
//	  GET    /v1/scopes/agent-policies/{agent_id}   — get one binding
//	  DELETE /v1/scopes/agent-policies/{agent_id}   — remove a binding
//
// The CORE is retrieval.Retriever's PutAgentPolicy/DeleteAgentPolicy (which also
// invalidate the affected agent's cached reads, §6 blocking #2) and
// GetAgentPolicy/ListAgentPolicies (pure reads) — validation (agent_id required,
// effect constrained) lives in the TopicViewStore drivers, proven identically by
// conformance, so these handlers are thin callers (mirrors memory_agent_policy on
// MCP; D-067/D-073).

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/store"
)

// agentPolicyResponse is the wire format for an agent->topic policy binding.
type agentPolicyResponse struct {
	AgentID     string   `json:"agent_id"`
	AllowTopics []string `json:"allow_topics,omitempty"`
	DenyTopics  []string `json:"deny_topics,omitempty"`
	CreatedAt   int64    `json:"created_at,omitempty"`
	UpdatedAt   int64    `json:"updated_at,omitempty"`
}

func agentPolicyToWire(p store.AgentPolicy) agentPolicyResponse {
	return agentPolicyResponse{
		AgentID: p.AgentID, AllowTopics: p.AllowTopics, DenyTopics: p.DenyTopics,
		CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt,
	}
}

// requireRetrieverForAgentPolicy returns false and writes a 503 if the retriever
// (the agent-policy core, D-067/D-073) is not wired.
func (s *Server) requireRetrieverForAgentPolicy(w http.ResponseWriter) bool {
	if s.retriever == nil {
		respondJSON(w, http.StatusServiceUnavailable, errBody("agent-policy admin not available"))
		return false
	}
	return true
}

// putAgentPolicyRequest is the wire format for PUT /v1/scopes/agent-policies.
type putAgentPolicyRequest struct {
	AgentID     string   `json:"agent_id"`
	AllowTopics []string `json:"allow_topics"`
	DenyTopics  []string `json:"deny_topics"`
}

// handlePutAgentPolicy implements PUT /v1/scopes/agent-policies (upsert — atomic
// replace of the agent's allow/deny topic-key sets).
func (s *Server) handlePutAgentPolicy(w http.ResponseWriter, r *http.Request) {
	if !s.requireRetrieverForAgentPolicy(w) {
		return
	}
	if !requireJSON(w, r) {
		return
	}
	authKey := keyFromContext(r.Context())
	tenantScope := identity.Scope{Tenant: authKey.TenantID}

	var req putAgentPolicyRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		respondJSON(w, http.StatusBadRequest, errBody("decode: "+sanitizeDecodeErr(err)))
		return
	}
	if req.AgentID == "" {
		respondJSON(w, http.StatusBadRequest, errBody("agent_id is required"))
		return
	}

	p := store.AgentPolicy{AgentID: req.AgentID, AllowTopics: req.AllowTopics, DenyTopics: req.DenyTopics}
	if err := s.retriever.PutAgentPolicy(r.Context(), tenantScope, p); err != nil {
		respondJSON(w, http.StatusInternalServerError, errBody("put agent policy: "+err.Error()))
		return
	}
	got, err := s.retriever.GetAgentPolicy(r.Context(), tenantScope, req.AgentID)
	if err != nil {
		respondJSON(w, http.StatusInternalServerError, errBody("put agent policy (read-back): "+err.Error()))
		return
	}
	respondJSON(w, http.StatusOK, agentPolicyToWire(*got))
}

// handleListAgentPolicies implements GET /v1/scopes/agent-policies.
func (s *Server) handleListAgentPolicies(w http.ResponseWriter, r *http.Request) {
	if !s.requireRetrieverForAgentPolicy(w) {
		return
	}
	authKey := keyFromContext(r.Context())
	tenantScope := identity.Scope{Tenant: authKey.TenantID}

	list, err := s.retriever.ListAgentPolicies(r.Context(), tenantScope)
	if err != nil {
		respondJSON(w, http.StatusInternalServerError, errBody("list agent policies: "+err.Error()))
		return
	}
	out := make([]agentPolicyResponse, len(list))
	for i, p := range list {
		out[i] = agentPolicyToWire(p)
	}
	respondJSON(w, http.StatusOK, map[string]interface{}{"policies": out})
}

// handleGetAgentPolicy implements GET /v1/scopes/agent-policies/{agent_id}.
func (s *Server) handleGetAgentPolicy(w http.ResponseWriter, r *http.Request) {
	if !s.requireRetrieverForAgentPolicy(w) {
		return
	}
	authKey := keyFromContext(r.Context())
	tenantScope := identity.Scope{Tenant: authKey.TenantID}
	agentID := r.PathValue("agent_id")
	if agentID == "" {
		respondJSON(w, http.StatusBadRequest, errBody("agent_id is required"))
		return
	}

	p, err := s.retriever.GetAgentPolicy(r.Context(), tenantScope, agentID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			respondJSON(w, http.StatusNotFound, errBody("agent policy not found"))
			return
		}
		respondJSON(w, http.StatusInternalServerError, errBody("get agent policy: "+err.Error()))
		return
	}
	respondJSON(w, http.StatusOK, agentPolicyToWire(*p))
}

// handleDeleteAgentPolicy implements DELETE /v1/scopes/agent-policies/{agent_id}.
func (s *Server) handleDeleteAgentPolicy(w http.ResponseWriter, r *http.Request) {
	if !s.requireRetrieverForAgentPolicy(w) {
		return
	}
	authKey := keyFromContext(r.Context())
	tenantScope := identity.Scope{Tenant: authKey.TenantID}
	agentID := r.PathValue("agent_id")
	if agentID == "" {
		respondJSON(w, http.StatusBadRequest, errBody("agent_id is required"))
		return
	}

	if err := s.retriever.DeleteAgentPolicy(r.Context(), tenantScope, agentID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			respondJSON(w, http.StatusNotFound, errBody("agent policy not found"))
			return
		}
		respondJSON(w, http.StatusInternalServerError, errBody("delete agent policy: "+err.Error()))
		return
	}
	respondJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}
