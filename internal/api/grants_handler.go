package api

// grants_handler.go — HTTP handlers for grants and groups (Phase 15, RFC §5.3).
//
// Routes (registered in server.go):
//
//	Admin-only (requires admin key):
//	  POST   /v1/admin/groups              — create a group
//	  GET    /v1/admin/groups              — list groups
//	  POST   /v1/admin/groups/{id}/members — add member to group
//	  DELETE /v1/admin/groups/{id}/members/{user_id} — remove member
//
//	Scope-aware (agent or admin key):
//	  GET    /v1/scopes/grants             — list grants for the caller's tenant
//	  PUT    /v1/scopes/grants             — create a grant (validates zone ceiling, AC-2)
//	  POST   /v1/grants/{id}/revoke        — revoke a grant

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/hurtener/stowage/internal/grants"
	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/store"
)

// --- Wire types ---

type groupResponse struct {
	ID        string `json:"id"`
	TenantID  string `json:"tenant_id"`
	Name      string `json:"name"`
	CreatedAt int64  `json:"created_at"`
}

func groupToWire(g store.Group) groupResponse {
	return groupResponse{
		ID:        g.ID,
		TenantID:  g.TenantID,
		Name:      g.Name,
		CreatedAt: g.CreatedAt,
	}
}

type memberResponse struct {
	ID        string `json:"id"`
	GroupID   string `json:"group_id"`
	UserID    string `json:"user_id"`
	TenantID  string `json:"tenant_id"`
	CreatedAt int64  `json:"created_at"`
}

func memberToWire(m store.GroupMember) memberResponse {
	return memberResponse{
		ID:        m.ID,
		GroupID:   m.GroupID,
		UserID:    m.UserID,
		TenantID:  m.TenantID,
		CreatedAt: m.CreatedAt,
	}
}

type grantResponse struct {
	ID               string `json:"id"`
	TenantID         string `json:"tenant_id"`
	ProjectID        string `json:"project_id,omitempty"`
	UserID           string `json:"user_id,omitempty"`
	SessionID        string `json:"session_id,omitempty"`
	GroupID          string `json:"group_id"`
	Access           string `json:"access"`
	TopicFilter      string `json:"topic_filter,omitempty"`
	KindFilter       string `json:"kind_filter,omitempty"`
	ZoneCeiling      string `json:"zone_ceiling"`
	RedactionProfile string `json:"redaction_profile,omitempty"`
	RevokedAt        int64  `json:"revoked_at,omitempty"`
	CreatedAt        int64  `json:"created_at"`
	UpdatedAt        int64  `json:"updated_at"`
}

func grantToWire(g store.Grant) grantResponse {
	return grantResponse{
		ID:               g.ID,
		TenantID:         g.TenantID,
		ProjectID:        g.ProjectID,
		UserID:           g.UserID,
		SessionID:        g.SessionID,
		GroupID:          g.GroupID,
		Access:           g.Access,
		TopicFilter:      g.TopicFilter,
		KindFilter:       g.KindFilter,
		ZoneCeiling:      g.ZoneCeiling,
		RedactionProfile: g.RedactionProfile,
		RevokedAt:        g.RevokedAt,
		CreatedAt:        g.CreatedAt,
		UpdatedAt:        g.UpdatedAt,
	}
}

// requireGrantsSvc returns false and writes a 503 if the grants service is not
// wired. Handlers call this at the top of every grants/groups handler.
func (s *Server) requireGrantsSvc(w http.ResponseWriter) bool {
	if s.grantsSvc == nil {
		respondJSON(w, http.StatusServiceUnavailable, errBody("grants service not available"))
		return false
	}
	return true
}

// --- Admin: group management ---

// handleCreateGroup implements POST /v1/admin/groups.
func (s *Server) handleCreateGroup(w http.ResponseWriter, r *http.Request) {
	if !s.requireGrantsSvc(w) {
		return
	}
	if !requireJSON(w, r) {
		return
	}
	authKey := keyFromContext(r.Context())
	scope := identity.Scope{Tenant: authKey.TenantID}

	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondJSON(w, http.StatusBadRequest, errBody("decode: "+err.Error()))
		return
	}
	if req.Name == "" {
		respondJSON(w, http.StatusBadRequest, errBody("name is required"))
		return
	}

	g, err := s.grantsSvc.CreateGroup(r.Context(), scope, req.Name)
	if err != nil {
		respondJSON(w, http.StatusInternalServerError, errBody("create group: "+err.Error()))
		return
	}
	respondJSON(w, http.StatusCreated, groupToWire(*g))
}

// handleListGroups implements GET /v1/admin/groups.
func (s *Server) handleListGroups(w http.ResponseWriter, r *http.Request) {
	if !s.requireGrantsSvc(w) {
		return
	}
	authKey := keyFromContext(r.Context())
	scope := identity.Scope{Tenant: authKey.TenantID}

	grps, err := s.grantsSvc.ListGroups(r.Context(), scope)
	if err != nil {
		respondJSON(w, http.StatusInternalServerError, errBody("list groups: "+err.Error()))
		return
	}
	out := make([]groupResponse, len(grps))
	for i, g := range grps {
		out[i] = groupToWire(g)
	}
	respondJSON(w, http.StatusOK, map[string]interface{}{"groups": out})
}

// handleAddMember implements POST /v1/admin/groups/{id}/members.
func (s *Server) handleAddMember(w http.ResponseWriter, r *http.Request) {
	if !s.requireGrantsSvc(w) {
		return
	}
	if !requireJSON(w, r) {
		return
	}
	authKey := keyFromContext(r.Context())
	scope := identity.Scope{Tenant: authKey.TenantID}
	groupID := r.PathValue("id")
	if groupID == "" {
		respondJSON(w, http.StatusBadRequest, errBody("group id is required"))
		return
	}

	var req struct {
		UserID string `json:"user_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondJSON(w, http.StatusBadRequest, errBody("decode: "+err.Error()))
		return
	}
	if req.UserID == "" {
		respondJSON(w, http.StatusBadRequest, errBody("user_id is required"))
		return
	}

	m, err := s.grantsSvc.AddMember(r.Context(), scope, groupID, req.UserID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			respondJSON(w, http.StatusNotFound, errBody("group not found"))
			return
		}
		respondJSON(w, http.StatusInternalServerError, errBody("add member: "+err.Error()))
		return
	}
	respondJSON(w, http.StatusCreated, memberToWire(*m))
}

// handleRemoveMember implements DELETE /v1/admin/groups/{id}/members/{user_id}.
func (s *Server) handleRemoveMember(w http.ResponseWriter, r *http.Request) {
	if !s.requireGrantsSvc(w) {
		return
	}
	authKey := keyFromContext(r.Context())
	scope := identity.Scope{Tenant: authKey.TenantID}
	groupID := r.PathValue("id")
	userID := r.PathValue("user_id")
	if groupID == "" || userID == "" {
		respondJSON(w, http.StatusBadRequest, errBody("group id and user_id are required"))
		return
	}

	if err := s.grantsSvc.RemoveMember(r.Context(), scope, groupID, userID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			respondJSON(w, http.StatusNotFound, errBody("member not found"))
			return
		}
		respondJSON(w, http.StatusInternalServerError, errBody("remove member: "+err.Error()))
		return
	}
	respondJSON(w, http.StatusNoContent, nil)
}

// --- Grant management ---

// handleListGrants implements GET /v1/scopes/grants.
func (s *Server) handleListGrants(w http.ResponseWriter, r *http.Request) {
	if !s.requireGrantsSvc(w) {
		return
	}
	authKey := keyFromContext(r.Context())
	tenantScope := identity.Scope{Tenant: authKey.TenantID}

	gs, err := s.grantsSvc.ListGrants(r.Context(), tenantScope)
	if err != nil {
		respondJSON(w, http.StatusInternalServerError, errBody("list grants: "+err.Error()))
		return
	}
	out := make([]grantResponse, len(gs))
	for i, g := range gs {
		out[i] = grantToWire(g)
	}
	respondJSON(w, http.StatusOK, map[string]interface{}{"grants": out})
}

// createGrantRequest is the wire format for PUT /v1/scopes/grants.
type createGrantRequest struct {
	// OwnerScope fields define whose memories are being shared.
	ProjectID        string `json:"project_id"`
	UserID           string `json:"user_id"`
	SessionID        string `json:"session_id"`
	GroupID          string `json:"group_id"`
	Access           string `json:"access"`            // "read" | "contribute"
	TopicFilter      string `json:"topic_filter"`      // "" = all topics
	KindFilter       string `json:"kind_filter"`       // "" = all kinds
	ZoneCeiling      string `json:"zone_ceiling"`      // "public" | "work"
	RedactionProfile string `json:"redaction_profile"` // stub
}

// handleCreateGrant implements PUT /v1/scopes/grants.
// Validates: same-tenant, zone_ceiling ∈ {public,work}, access ∈ {read,contribute}.
func (s *Server) handleCreateGrant(w http.ResponseWriter, r *http.Request) {
	if !s.requireGrantsSvc(w) {
		return
	}
	if !requireJSON(w, r) {
		return
	}
	authKey := keyFromContext(r.Context())
	callerScope := identity.Scope{Tenant: authKey.TenantID}

	var req createGrantRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		respondJSON(w, http.StatusBadRequest, errBody("decode: "+sanitizeDecodeErr(err)))
		return
	}
	if req.GroupID == "" {
		respondJSON(w, http.StatusBadRequest, errBody("group_id is required"))
		return
	}
	if req.ZoneCeiling == "" {
		respondJSON(w, http.StatusBadRequest, errBody("zone_ceiling is required (public or work)"))
		return
	}
	if req.Access == "" {
		req.Access = "read" // default
	}

	in := grants.CreateGrantInput{
		OwnerScope: identity.Scope{
			Tenant:  callerScope.Tenant, // owner must be same tenant
			Project: req.ProjectID,
			User:    req.UserID,
			Session: req.SessionID,
		},
		GroupID:          req.GroupID,
		Access:           req.Access,
		TopicFilter:      req.TopicFilter,
		KindFilter:       req.KindFilter,
		ZoneCeiling:      req.ZoneCeiling,
		RedactionProfile: req.RedactionProfile,
	}

	g, err := s.grantsSvc.CreateGrant(r.Context(), callerScope, in)
	if err != nil {
		switch {
		case errors.Is(err, grants.ErrCrossTenantGrant):
			respondJSON(w, http.StatusBadRequest, errBody("cross-tenant grants are not allowed"))
		case errors.Is(err, grants.ErrInvalidZoneCeiling):
			respondJSON(w, http.StatusBadRequest, errBody("zone_ceiling must be 'public' or 'work'"))
		case errors.Is(err, grants.ErrInvalidAccess):
			respondJSON(w, http.StatusBadRequest, errBody("access must be 'read' or 'contribute'"))
		default:
			respondJSON(w, http.StatusInternalServerError, errBody("create grant: "+err.Error()))
		}
		return
	}
	respondJSON(w, http.StatusCreated, grantToWire(*g))
}

// handleRevokeGrant implements POST /v1/grants/{id}/revoke.
func (s *Server) handleRevokeGrant(w http.ResponseWriter, r *http.Request) {
	if !s.requireGrantsSvc(w) {
		return
	}
	authKey := keyFromContext(r.Context())
	tenantScope := identity.Scope{Tenant: authKey.TenantID}
	id := r.PathValue("id")
	if id == "" {
		respondJSON(w, http.StatusBadRequest, errBody("grant id is required"))
		return
	}

	if err := s.grantsSvc.RevokeGrant(r.Context(), tenantScope, id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			respondJSON(w, http.StatusNotFound, errBody("grant not found or already revoked"))
			return
		}
		respondJSON(w, http.StatusInternalServerError, errBody("revoke grant: "+err.Error()))
		return
	}
	respondJSON(w, http.StatusOK, map[string]string{"status": "revoked"})
}
