package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/store"
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
	// Source is "explicit" for user-created topics, "pack" for virtual defaults (D-043).
	Source string `json:"source"`
}

// handleListTopics implements GET /v1/topics.
//
// Returns the effective topic set for the authenticated scope:
//   - Explicit active topics when any exist.
//   - The profile's virtual default pack when the scope has none (D-043).
//   - Empty list when pack:off is the only active topic.
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
	now := time.Now().UnixMilli()

	for i, item := range items {
		if item.Key == "" {
			respondJSON(w, http.StatusBadRequest,
				errBody(fmt.Sprintf("item[%d]: key must not be empty", i)))
			return
		}
		status := item.Status
		if status == "" {
			status = "active"
		}
		if status != "active" && status != "paused" {
			respondJSON(w, http.StatusBadRequest,
				errBody(fmt.Sprintf("item[%d]: status must be active or paused", i)))
			return
		}
		t := store.Topic{
			ID:          ulid.Make().String(),
			TenantID:    authKey.TenantID,
			Key:         item.Key,
			Description: item.Description,
			Status:      status,
			CreatedAt:   now,
			UpdatedAt:   now,
		}
		if err := s.st.Topics().Upsert(r.Context(), scope, t); err != nil {
			s.log.ErrorContext(r.Context(), "api: topics: upsert failed",
				"key", item.Key, "err", err)
			respondJSON(w, http.StatusInternalServerError, errBody("store error"))
			return
		}
	}

	respondJSON(w, http.StatusOK, map[string]int{"upserted": len(items)})
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

	authKey := keyFromContext(r.Context())
	scope := identity.Scope{Tenant: authKey.TenantID}

	if err := s.st.Topics().Delete(r.Context(), scope, key); err != nil {
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
