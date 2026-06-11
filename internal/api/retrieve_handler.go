package api

import (
	"encoding/json"
	"net/http"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/retrieval"
	"github.com/hurtener/stowage/internal/store"
)

// retrieveRequest is the wire format for POST /v1/retrieve.
type retrieveRequest struct {
	Query        string   `json:"query"`
	Limit        int      `json:"limit"`
	From         int64    `json:"from"`  // unix millis; 0 = unbounded
	Until        int64    `json:"until"` // unix millis; 0 = unbounded
	Kinds        []string `json:"kinds"`
	IncludeLanes bool     `json:"include_lanes"`
}

// retrieveMemoryItem is the wire format for one retrieval result.
type retrieveMemoryItem struct {
	ID      string   `json:"id"`
	Kind    string   `json:"kind"`
	Content string   `json:"content"`
	Context string   `json:"context,omitempty"`
	Score   float64  `json:"score"`
	Lanes   []string `json:"lanes,omitempty"`
}

// retrieveResponse is the wire format for POST /v1/retrieve.
type retrieveResponse struct {
	Items    []retrieveMemoryItem `json:"items"`
	Degraded bool                 `json:"degraded"`
	API      string               `json:"api"`
}

// handleRetrieve implements POST /v1/retrieve.
//
// The handler authenticates the caller (any valid key), extracts the tenant
// scope, calls retrieval.Retriever.Retrieve, and returns the fused results.
// When the vector lane is unavailable the response still returns 200 with
// degraded:true (D-036).
func (s *Server) handleRetrieve(w http.ResponseWriter, r *http.Request) {
	if !requireJSON(w, r) {
		return
	}

	var req retrieveRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		respondJSON(w, http.StatusBadRequest, errBody("decode: "+sanitizeDecodeErr(err)))
		return
	}

	if req.Query == "" {
		respondJSON(w, http.StatusBadRequest, errBody("query must not be empty"))
		return
	}

	authKey := keyFromContext(r.Context())
	scope := identity.Scope{Tenant: authKey.TenantID}

	if s.retriever == nil {
		respondJSON(w, http.StatusServiceUnavailable, errBody("retrieval not available"))
		return
	}

	resp, err := s.retriever.Retrieve(r.Context(), scope, retrieval.Request{
		Query:        req.Query,
		Limit:        req.Limit,
		Window:       store.Window{From: req.From, Until: req.Until},
		Kinds:        req.Kinds,
		IncludeLanes: req.IncludeLanes,
	})
	if err != nil {
		s.log.ErrorContext(r.Context(), "api: retrieve failed", "err", err)
		respondJSON(w, http.StatusInternalServerError, errBody("retrieve error"))
		return
	}

	items := make([]retrieveMemoryItem, 0, len(resp.Items))
	for _, item := range resp.Items {
		ri := retrieveMemoryItem{
			ID:      item.Memory.ID,
			Kind:    item.Memory.Kind,
			Content: item.Memory.Content,
			Context: item.Memory.Context,
			Score:   item.Score,
		}
		if req.IncludeLanes {
			ri.Lanes = item.Lanes
		}
		items = append(items, ri)
	}

	respondJSON(w, http.StatusOK, retrieveResponse{
		Items:    items,
		Degraded: resp.Degraded,
		API:      resp.API,
	})
}
