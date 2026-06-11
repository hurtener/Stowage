package api

import (
	"encoding/json"
	"net/http"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/pipeline"
)

// flushRequest is the body for POST /v1/buffers/{key}/flush.
type flushRequest struct {
	// Trigger must be "explicit" or "session_end". Defaults to "explicit".
	Trigger string `json:"trigger"`
}

// flushResponse is returned on a successful flush request.
type flushResponse struct {
	Key     string `json:"key"`
	Trigger string `json:"trigger"`
	Flushed bool   `json:"flushed"`
}

// handleFlushBuffer implements POST /v1/buffers/{key}/flush.
//
// An explicit flush drains all unflushed items for the buffer key and emits
// a FlushedBuffer event on the downstream channel. Returns 202 Accepted.
// The stage may be nil (tests); in that case the endpoint is a 202 no-op.
func (s *Server) handleFlushBuffer(w http.ResponseWriter, r *http.Request) {
	if !requireJSON(w, r) {
		return
	}

	bufferKey := r.PathValue("key")
	if bufferKey == "" {
		respondJSON(w, http.StatusBadRequest, errBody("buffer key is required"))
		return
	}

	var req flushRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondJSON(w, http.StatusBadRequest, errBody("decode: "+err.Error()))
		return
	}

	trigger := req.Trigger
	switch trigger {
	case "", pipeline.TriggerExplicit:
		trigger = pipeline.TriggerExplicit
	case pipeline.TriggerSessionEnd:
		// valid
	default:
		respondJSON(w, http.StatusBadRequest,
			errBody("trigger must be explicit or session_end"))
		return
	}

	authKey := keyFromContext(r.Context())
	scope := identity.Scope{Tenant: authKey.TenantID}

	flushed := false
	if s.stage != nil {
		if err := s.stage.FlushKey(r.Context(), scope, bufferKey, trigger); err != nil {
			s.log.ErrorContext(r.Context(), "api: flush buffer: error",
				"key", bufferKey, "err", err)
			respondJSON(w, http.StatusInternalServerError, errBody("flush error"))
			return
		}
		flushed = true
	}

	respondJSON(w, http.StatusAccepted, flushResponse{
		Key:     bufferKey,
		Trigger: trigger,
		Flushed: flushed,
	})
}
