package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/hurtener/stowage/internal/auth"
	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/store"
)

// keyInfo is the wire representation of an API key (no hash, no plaintext).
type keyInfo struct {
	ID        string `json:"id"`
	TenantID  string `json:"tenant_id"`
	Role      string `json:"role"`
	CreatedAt int64  `json:"created_at"` // unix millis
	RevokedAt *int64 `json:"revoked_at"` // nil if active; unix millis if revoked
}

func keyToInfo(k auth.Key) keyInfo {
	ki := keyInfo{
		ID:        k.ID,
		TenantID:  k.TenantID,
		Role:      string(k.Role),
		CreatedAt: k.CreatedAt.UnixMilli(),
	}
	if k.RevokedAt != nil {
		ms := k.RevokedAt.UnixMilli()
		ki.RevokedAt = &ms
	}
	return ki
}

// handleCreateKey implements POST /v1/admin/keys.
// Returns the new key's metadata plus the plaintext shown once (CLAUDE.md §7).
//
// Bootstrap mode: if the keyring is completely empty (first-boot, no keys
// exist yet), one key may be created without an Authorization header. On all
// subsequent calls an admin Bearer token is required. This prevents the
// chicken-and-egg problem of "how do I get the first key".
func (s *Server) handleCreateKey(w http.ResponseWriter, r *http.Request) {
	if !requireJSON(w, r) {
		return
	}

	var req struct {
		TenantID string `json:"tenant_id"`
		Role     string `json:"role"` // "agent" | "admin"
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondJSON(w, http.StatusBadRequest, errBody("decode: "+err.Error()))
		return
	}
	if req.TenantID == "" {
		respondJSON(w, http.StatusBadRequest, errBody("tenant_id is required"))
		return
	}
	role := auth.Role(req.Role)
	if role != auth.RoleAgent && role != auth.RoleAdmin {
		respondJSON(w, http.StatusBadRequest, errBody(`role must be "agent" or "admin"`))
		return
	}

	// Auth check: require admin key, UNLESS this is a bootstrap (keyring empty).
	hdr := r.Header.Get("Authorization")
	if hdr != "" {
		// Normal authenticated path.
		pt, ok := strings.CutPrefix(hdr, "Bearer ")
		if !ok {
			respondJSON(w, http.StatusUnauthorized, errBody("Authorization must be Bearer sk_..."))
			return
		}
		callerKey, verifyErr := auth.Verify(s.st.Keys(), pt)
		if verifyErr != nil {
			respondJSON(w, http.StatusUnauthorized, errBody("invalid or revoked key"))
			return
		}
		if callerKey.Role != auth.RoleAdmin {
			respondJSON(w, http.StatusForbidden, errBody("admin role required"))
			return
		}
	} else {
		// No auth header: only allow if the keyring is empty (bootstrap).
		// Once any key exists this path returns 401.
		existing, listErr := s.st.Keys().List("")
		if listErr != nil {
			s.log.ErrorContext(r.Context(), "api: create key: bootstrap check failed", "err", listErr)
			respondJSON(w, http.StatusInternalServerError, errBody("store error"))
			return
		}
		if len(existing) > 0 {
			respondJSON(w, http.StatusUnauthorized, errBody("missing Authorization header"))
			return
		}
		s.log.InfoContext(r.Context(), "api: create key: bootstrap — keyring was empty, creating first key")
	}

	key, plaintext, err := auth.Generate(req.TenantID, role)
	if err != nil {
		s.log.ErrorContext(r.Context(), "api: create key: generate failed", "err", err)
		respondJSON(w, http.StatusInternalServerError, errBody("key generation failed"))
		return
	}
	if err := s.st.Keys().Insert(key); err != nil {
		s.log.ErrorContext(r.Context(), "api: create key: insert failed", "err", err)
		respondJSON(w, http.StatusInternalServerError, errBody("store error"))
		return
	}

	// Plaintext is returned ONCE. Caller must store it; server never logs it
	// (CLAUDE.md §7 — keys are never logged).
	respondJSON(w, http.StatusCreated, struct {
		Key       keyInfo `json:"key"`
		Plaintext string  `json:"plaintext"`
	}{
		Key:       keyToInfo(key),
		Plaintext: plaintext,
	})
}

// handleListKeys implements GET /v1/admin/keys.
// Lists all keys for the authenticated key's tenant. No hashes or plaintexts.
func (s *Server) handleListKeys(w http.ResponseWriter, r *http.Request) {
	authKey := keyFromContext(r.Context())
	keys, err := s.st.Keys().List(authKey.TenantID)
	if err != nil {
		s.log.ErrorContext(r.Context(), "api: list keys: failed", "err", err)
		respondJSON(w, http.StatusInternalServerError, errBody("store error"))
		return
	}
	infos := make([]keyInfo, len(keys))
	for i, k := range keys {
		infos[i] = keyToInfo(k)
	}
	respondJSON(w, http.StatusOK, struct {
		Keys []keyInfo `json:"keys"`
	}{Keys: infos})
}

// handleRevokeKey implements POST /v1/admin/keys/{id}/revoke.
func (s *Server) handleRevokeKey(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		respondJSON(w, http.StatusBadRequest, errBody("key id is required"))
		return
	}
	if err := s.st.Keys().Revoke(id, time.Now().UTC()); err != nil {
		if errors.Is(err, auth.ErrKeyNotFound) {
			respondJSON(w, http.StatusNotFound, errBody("key not found"))
			return
		}
		s.log.ErrorContext(r.Context(), "api: revoke key: failed", "err", err)
		respondJSON(w, http.StatusInternalServerError, errBody("store error"))
		return
	}
	respondJSON(w, http.StatusOK, struct{}{})
}

// handleRevokeTenantKeys implements POST /v1/admin/keys/revoke-tenant.
// Revokes all active keys for the given tenant_id. Effective immediately —
// no restart required (AC-6, D-030 — keyring is the live store).
func (s *Server) handleRevokeTenantKeys(w http.ResponseWriter, r *http.Request) {
	if !requireJSON(w, r) {
		return
	}

	var req struct {
		TenantID string `json:"tenant_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondJSON(w, http.StatusBadRequest, errBody("decode: "+err.Error()))
		return
	}
	if req.TenantID == "" {
		respondJSON(w, http.StatusBadRequest, errBody("tenant_id is required"))
		return
	}

	keys, err := s.st.Keys().List(req.TenantID)
	if err != nil {
		s.log.ErrorContext(r.Context(), "api: revoke-tenant: list failed", "err", err)
		respondJSON(w, http.StatusInternalServerError, errBody("store error"))
		return
	}

	now := time.Now().UTC()
	count := 0
	for _, k := range keys {
		if k.RevokedAt != nil {
			continue // already revoked
		}
		if err := s.st.Keys().Revoke(k.ID, now); err != nil {
			if errors.Is(err, auth.ErrKeyNotFound) {
				continue // concurrent revoke; safe to skip
			}
			s.log.ErrorContext(r.Context(), "api: revoke-tenant: revoke failed",
				"key_id", k.ID, "err", err)
			respondJSON(w, http.StatusInternalServerError, errBody("store error"))
			return
		}
		count++
	}

	respondJSON(w, http.StatusOK, struct {
		Count int `json:"count"`
	}{Count: count})
}

// handleDSAR implements DELETE /v1/admin/users/{user} — the DSAR (Data Subject
// Access Request) cascading delete of ALL data for one (tenant, user) (RFC §13,
// D-098). Admin-only (authMiddleware admin=true). The tenant is taken from the
// caller's admin key — an admin can only purge users within their own tenant
// (P3); the {user} path segment names the data subject. This is the only API
// path that deletes verbatim records (the P1 retention/DSAR cascade exception).
func (s *Server) handleDSAR(w http.ResponseWriter, r *http.Request) {
	user := r.PathValue("user")
	if user == "" {
		respondJSON(w, http.StatusBadRequest, errBody("user is required"))
		return
	}
	authKey := keyFromContext(r.Context())
	scope := identity.Scope{Tenant: authKey.TenantID, User: user}

	counts, err := s.st.Ops().DeleteUserData(r.Context(), scope)
	if err != nil {
		if errors.Is(err, store.ErrScopeRequired) {
			respondJSON(w, http.StatusBadRequest, errBody("tenant and user are required"))
			return
		}
		s.log.ErrorContext(r.Context(), "api: dsar purge failed",
			"tenant", authKey.TenantID, "user", user, "err", err)
		respondJSON(w, http.StatusInternalServerError, errBody("store error"))
		return
	}

	// Audit at INFO (no secrets): the store also emits a tenant-scoped
	// user.purged event with the same counts.
	s.log.InfoContext(r.Context(), "api: dsar purge complete",
		"tenant", authKey.TenantID, "user", user,
		"memories", counts.Memories, "records", counts.Records)

	respondJSON(w, http.StatusOK, struct {
		User   string           `json:"user"`
		Counts store.DSARCounts `json:"counts"`
	}{User: user, Counts: counts})
}
