package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/topics"
)

// topicInput is the per-item wire format for PUT /v1/topics.
type topicInput struct {
	Key         string `json:"key"`
	Description string `json:"description"`
	// Status defaults to "active" when omitted. Accepted values: "active", "paused".
	Status string `json:"status"`
}

// topicView is the per-item wire format for GET /v1/topics.
type topicView struct {
	Key         string `json:"key"`
	Description string `json:"description"`
	Status      string `json:"status"`
	Pack        string `json:"pack,omitempty"`
	// Source is "explicit" for user-created topics, or the pack name (e.g.
	// "pack:project") for entries contributed by an enabled pack (D-043 introduced
	// "pack"; D-099 widened it to the specific pack name).
	Source string `json:"source"`
}

// handleListTopics implements GET /v1/topics.
//
// Returns the effective COMPOSED topic set for the authenticated scope (D-099):
// the deduped union of the scope's explicit topics and the entries of every enabled
// pack (explicit wins key collisions), capped at the composition limit. The
// profile's default pack list applies only when the scope has expressed no intent;
// `pack:off` suppresses packs (leaving explicit topics); an empty result is returned
// as an empty list.
func (s *Server) handleListTopics(w http.ResponseWriter, r *http.Request) {
	authKey := keyFromContext(r.Context())
	scope := identity.Scope{Tenant: authKey.TenantID}

	if s.topicSvc == nil {
		respondJSON(w, http.StatusServiceUnavailable, errBody("topics service not configured"))
		return
	}

	views, err := s.topicSvc.ActiveTopics(r.Context(), scope)
	if err != nil {
		s.log.ErrorContext(r.Context(), "api: topics: list failed", "err", err)
		respondJSON(w, http.StatusInternalServerError, errBody("store error"))
		return
	}

	out := make([]topicView, 0, len(views))
	for _, v := range views {
		out = append(out, topicView{
			Key:         v.Key,
			Description: v.Description,
			Status:      v.Status,
			Pack:        v.Pack,
			Source:      v.Source,
		})
	}
	respondJSON(w, http.StatusOK, map[string][]topicView{"topics": out})
}

// handleUpsertTopics implements PUT /v1/topics.
//
// Body: JSON array of {key, description, status}. Upserts each topic by key
// within the authenticated tenant scope. Returns 200 {upserted: N}.
func (s *Server) handleUpsertTopics(w http.ResponseWriter, r *http.Request) {
	if !requireJSON(w, r) {
		return
	}

	if s.topicSvc == nil {
		respondJSON(w, http.StatusServiceUnavailable, errBody("topics service not configured"))
		return
	}

	var items []topicInput
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&items); err != nil {
		respondJSON(w, http.StatusBadRequest, errBody("decode: "+err.Error()))
		return
	}
	if len(items) == 0 {
		respondJSON(w, http.StatusBadRequest, errBody("topics array must not be empty"))
		return
	}

	authKey := keyFromContext(r.Context())
	scope := identity.Scope{Tenant: authKey.TenantID}

	// Route through the shared topics.Service so active|paused validation is
	// enforced on every surface — one core, no per-surface drift (D-071,
	// Wave-B checkpoint). Validation failures map to 400, store errors to 500.
	upserts := make([]topics.TopicUpsert, len(items))
	for i, item := range items {
		upserts[i] = topics.TopicUpsert{
			Key:         item.Key,
			Description: item.Description,
			Status:      item.Status,
		}
	}
	n, err := s.topicSvc.Upsert(r.Context(), scope, upserts)
	if err != nil {
		if errors.Is(err, topics.ErrInvalidTopic) {
			respondJSON(w, http.StatusBadRequest, errBody(err.Error()))
			return
		}
		s.log.ErrorContext(r.Context(), "api: topics: upsert failed", "err", err)
		respondJSON(w, http.StatusInternalServerError, errBody("store error"))
		return
	}

	respondJSON(w, http.StatusOK, map[string]int{"upserted": n})
}

// handleDeleteTopic implements DELETE /v1/topics/{key}.
//
// Soft-deletes the topic (sets status = "deleted"). Returns 200 {deleted: key}.
// Returns 404 when the key does not exist in the scope.
func (s *Server) handleDeleteTopic(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if key == "" {
		respondJSON(w, http.StatusBadRequest, errBody("topic key is required"))
		return
	}

	if s.topicSvc == nil {
		respondJSON(w, http.StatusServiceUnavailable, errBody("topics service not configured"))
		return
	}

	authKey := keyFromContext(r.Context())
	scope := identity.Scope{Tenant: authKey.TenantID}

	// Route through the shared topics.Service (D-071, Wave-B checkpoint).
	if err := s.topicSvc.Delete(r.Context(), scope, key); err != nil {
		if isNotFound(err) {
			respondJSON(w, http.StatusNotFound, errBody("topic not found"))
			return
		}
		s.log.ErrorContext(r.Context(), "api: topics: delete failed", "key", key, "err", err)
		respondJSON(w, http.StatusInternalServerError, errBody("store error"))
		return
	}

	respondJSON(w, http.StatusOK, map[string]string{"deleted": key})
}
