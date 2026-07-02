package api

// views_handler.go — HTTP handlers for named topic views (Phase ae9,
// D-149/D-151). Mirrors grants_handler.go's / agentpolicy_handler.go's shape.
//
// Routes (registered in server.go):
//
//	Scope-aware (agent or admin key):
//	  GET    /v1/scopes/views                            — list the tenant's views (optionally ?subject_kind=&subject_id=)
//	  POST   /v1/scopes/views                             — create a view
//	  PUT    /v1/scopes/views                             — update a view (full allow/deny replace)
//	  DELETE /v1/scopes/views/{subject_kind}/{subject_id}/{view_name} — delete a view
//
// The CORE is views.Service (internal/views) — validation ((store.TopicView).
// Validate) and the governance audit-event emission live there, ONCE, so
// these handlers are thin callers and NEVER emit an event themselves
// (D-067/D-073, matching memory_views on MCP).

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/store"
)

// viewResponse is the wire format for a topic view.
type viewResponse struct {
	SubjectKind string   `json:"subject_kind"`
	SubjectID   string   `json:"subject_id"`
	ViewName    string   `json:"view_name"`
	AllowTopics []string `json:"allow_topics,omitempty"`
	DenyTopics  []string `json:"deny_topics,omitempty"`
	CreatedAt   int64    `json:"created_at,omitempty"`
	UpdatedAt   int64    `json:"updated_at,omitempty"`
}

func viewToWire(v store.TopicView) viewResponse {
	return viewResponse{
		SubjectKind: v.SubjectKind, SubjectID: v.SubjectID, ViewName: v.ViewName,
		AllowTopics: v.AllowTopics, DenyTopics: v.DenyTopics,
		CreatedAt: v.CreatedAt, UpdatedAt: v.UpdatedAt,
	}
}

// requireViewsSvc returns false and writes a 503 if the views service (the
// ae9 admin core, D-067/D-073) is not wired.
func (s *Server) requireViewsSvc(w http.ResponseWriter) bool {
	if s.viewsSvc == nil {
		respondJSON(w, http.StatusServiceUnavailable, errBody("views service not available"))
		return false
	}
	return true
}

// viewWriteRequest is the wire format for POST/PUT /v1/scopes/views.
type viewWriteRequest struct {
	SubjectKind string   `json:"subject_kind"`
	SubjectID   string   `json:"subject_id"`
	ViewName    string   `json:"view_name"`
	AllowTopics []string `json:"allow_topics"`
	DenyTopics  []string `json:"deny_topics"`
}

// handleCreateView implements POST /v1/scopes/views.
func (s *Server) handleCreateView(w http.ResponseWriter, r *http.Request) {
	if !s.requireViewsSvc(w) {
		return
	}
	if !requireJSON(w, r) {
		return
	}
	authKey := keyFromContext(r.Context())
	tenantScope := identity.Scope{Tenant: authKey.TenantID}

	var req viewWriteRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		respondJSON(w, http.StatusBadRequest, errBody("decode: "+sanitizeDecodeErr(err)))
		return
	}

	v, err := s.viewsSvc.CreateView(r.Context(), tenantScope, store.TopicView{
		SubjectKind: req.SubjectKind, SubjectID: req.SubjectID, ViewName: req.ViewName,
		AllowTopics: req.AllowTopics, DenyTopics: req.DenyTopics,
	})
	if err != nil {
		respondViewError(w, "create view", err)
		return
	}
	respondJSON(w, http.StatusCreated, viewToWire(*v))
}

// handleUpdateView implements PUT /v1/scopes/views.
func (s *Server) handleUpdateView(w http.ResponseWriter, r *http.Request) {
	if !s.requireViewsSvc(w) {
		return
	}
	if !requireJSON(w, r) {
		return
	}
	authKey := keyFromContext(r.Context())
	tenantScope := identity.Scope{Tenant: authKey.TenantID}

	var req viewWriteRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		respondJSON(w, http.StatusBadRequest, errBody("decode: "+sanitizeDecodeErr(err)))
		return
	}

	v, err := s.viewsSvc.UpdateView(r.Context(), tenantScope, store.TopicView{
		SubjectKind: req.SubjectKind, SubjectID: req.SubjectID, ViewName: req.ViewName,
		AllowTopics: req.AllowTopics, DenyTopics: req.DenyTopics,
	})
	if err != nil {
		respondViewError(w, "update view", err)
		return
	}
	respondJSON(w, http.StatusOK, viewToWire(*v))
}

// handleDeleteView implements DELETE /v1/scopes/views/{subject_kind}/{subject_id}/{view_name}.
func (s *Server) handleDeleteView(w http.ResponseWriter, r *http.Request) {
	if !s.requireViewsSvc(w) {
		return
	}
	authKey := keyFromContext(r.Context())
	tenantScope := identity.Scope{Tenant: authKey.TenantID}

	subjectKind := r.PathValue("subject_kind")
	subjectID := r.PathValue("subject_id")
	viewName := r.PathValue("view_name")
	if subjectKind == "" || subjectID == "" || viewName == "" {
		respondJSON(w, http.StatusBadRequest, errBody("subject_kind, subject_id, and view_name are required"))
		return
	}

	if err := s.viewsSvc.DeleteView(r.Context(), tenantScope, subjectKind, subjectID, viewName); err != nil {
		respondViewError(w, "delete view", err)
		return
	}
	respondJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// handleListViews implements GET /v1/scopes/views (optionally narrowed by
// ?subject_kind=&subject_id=, both required together).
func (s *Server) handleListViews(w http.ResponseWriter, r *http.Request) {
	if !s.requireViewsSvc(w) {
		return
	}
	authKey := keyFromContext(r.Context())
	tenantScope := identity.Scope{Tenant: authKey.TenantID}

	q := r.URL.Query()
	list, err := s.viewsSvc.ListViews(r.Context(), tenantScope, q.Get("subject_kind"), q.Get("subject_id"))
	if err != nil {
		respondViewError(w, "list views", err)
		return
	}
	out := make([]viewResponse, len(list))
	for i, v := range list {
		out[i] = viewToWire(v)
	}
	respondJSON(w, http.StatusOK, map[string]interface{}{"views": out})
}

// respondViewError maps a views.Service error to the appropriate HTTP status.
func respondViewError(w http.ResponseWriter, action string, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		respondJSON(w, http.StatusNotFound, errBody(action+": view not found"))
	case errors.Is(err, store.ErrConflict):
		respondJSON(w, http.StatusConflict, errBody(action+": a view already exists for this subject/view_name"))
	case errors.Is(err, store.ErrInvalidSubjectKind), errors.Is(err, store.ErrSubjectIDRequired), errors.Is(err, store.ErrEmptyPolicy):
		respondJSON(w, http.StatusBadRequest, errBody(action+": "+err.Error()))
	default:
		respondJSON(w, http.StatusInternalServerError, errBody(action+": "+err.Error()))
	}
}
