package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/hurtener/stowage/internal/identity"
)

// resolveRequest is the wire format for POST /v1/citations/resolve.
type resolveRequest struct {
	Citations []string `json:"citations"`
	// ProjectID/UserID scope the injection/memory reads to a sub-tenant identity (P3,
	// D-125); empty = tenant-wide. A citation outside the scope resolves to found:false.
	ProjectID string `json:"project_id"`
	UserID    string `json:"user_id"`
}

// resolveProvenanceRef is a single provenance span reference for a memory.
type resolveProvenanceRef struct {
	RecordID  string `json:"record_id"`
	SpanStart int    `json:"span_start"`
	SpanEnd   int    `json:"span_end"`
}

// resolveMemory is the memory summary included in a resolved citation.
type resolveMemory struct {
	ID         string  `json:"id"`
	Kind       string  `json:"kind"`
	Content    string  `json:"content"`
	Context    string  `json:"context,omitempty"`
	Importance int     `json:"importance"`
	Confidence float64 `json:"confidence"`
	CreatedAt  int64   `json:"created_at"`
}

// resolveItem is the per-citation result in the resolve response.
// When Found is false all other fields are zero/nil.
type resolveItem struct {
	Citation   string                 `json:"citation"`
	Found      bool                   `json:"found"`
	Memory     *resolveMemory         `json:"memory,omitempty"`
	Provenance []resolveProvenanceRef `json:"provenance,omitempty"`
	Rank       int                    `json:"rank,omitempty"`
	Score      float64                `json:"score,omitempty"`
	Lanes      []string               `json:"lanes,omitempty"`
}

// resolveResponse is the wire format for POST /v1/citations/resolve.
type resolveResponse struct {
	Items []resolveItem `json:"items"`
}

// handleCitationsResolve implements POST /v1/citations/resolve.
//
// Each citation handle (injection ULID, D-051) is resolved individually.
// Per-handle misses (not found or cross-tenant) set found:false without
// failing the batch (AC-5). The response preserves input order.
func (s *Server) handleCitationsResolve(w http.ResponseWriter, r *http.Request) {
	if !requireJSON(w, r) {
		return
	}

	var req resolveRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		respondJSON(w, http.StatusBadRequest, errBody("decode: "+sanitizeDecodeErr(err)))
		return
	}

	if len(req.Citations) == 0 {
		respondJSON(w, http.StatusBadRequest, errBody("citations must not be empty"))
		return
	}

	authKey := keyFromContext(r.Context())
	scope := identity.Scope{Tenant: authKey.TenantID, Project: req.ProjectID, User: req.UserID}

	// Resolve each citation to its injection, accumulating memory IDs.
	type resolvedInj struct {
		citation string
		memoryID string
		rank     int
		score    float64
		lane     string
	}
	resolved := make([]resolvedInj, 0, len(req.Citations))
	notFound := make(map[string]bool, len(req.Citations))

	for _, cit := range req.Citations {
		inj, err := s.st.Injections().Get(r.Context(), scope, cit)
		if err != nil {
			if isNotFound(err) {
				notFound[cit] = true
				continue
			}
			s.log.ErrorContext(r.Context(), "api: citations resolve get injection", "citation", cit, "err", err)
			notFound[cit] = true // treat internal errors as not-found for the caller
			continue
		}
		resolved = append(resolved, resolvedInj{
			citation: cit,
			memoryID: inj.MemoryID,
			rank:     inj.Rank,
			score:    inj.Score,
			lane:     inj.Lane,
		})
	}

	// Batch-fetch distinct memories.
	memIDSet := make(map[string]bool, len(resolved))
	memIDs := make([]string, 0, len(resolved))
	for _, ri := range resolved {
		if !memIDSet[ri.memoryID] {
			memIDSet[ri.memoryID] = true
			memIDs = append(memIDs, ri.memoryID)
		}
	}

	memByID := make(map[string]*resolveMemory, len(memIDs))
	provByMemID := make(map[string][]resolveProvenanceRef, len(memIDs))

	if len(memIDs) > 0 {
		mems, err := s.st.Memories().GetMany(r.Context(), scope, memIDs)
		if err != nil {
			s.log.ErrorContext(r.Context(), "api: citations resolve get memories", "err", err)
			// Fall through with empty memByID — all found injections will report found:false.
		} else {
			for _, m := range mems {
				mem := m // capture
				memByID[mem.ID] = &resolveMemory{
					ID:         mem.ID,
					Kind:       mem.Kind,
					Content:    mem.Content,
					Context:    mem.Context,
					Importance: mem.Importance,
					Confidence: mem.Confidence,
					CreatedAt:  mem.CreatedAt,
				}
			}
			// Fetch provenance for each memory that was found.
			for memID := range memByID {
				junctions, err := s.st.Memories().GetJunctions(r.Context(), scope, memID)
				if err != nil {
					// Non-fatal: return empty provenance for this memory.
					s.log.WarnContext(r.Context(), "api: citations resolve get junctions", "memory_id", memID, "err", err)
					provByMemID[memID] = []resolveProvenanceRef{}
					continue
				}
				refs := make([]resolveProvenanceRef, 0, len(junctions.Provenance))
				for _, p := range junctions.Provenance {
					refs = append(refs, resolveProvenanceRef{
						RecordID:  p.RecordID,
						SpanStart: p.SpanStart,
						SpanEnd:   p.SpanEnd,
					})
				}
				provByMemID[memID] = refs
			}
		}
	}

	// Build the injections index by citation for fast lookup.
	injByCit := make(map[string]resolvedInj, len(resolved))
	for _, ri := range resolved {
		injByCit[ri.citation] = ri
	}

	// Produce output in input order.
	items := make([]resolveItem, 0, len(req.Citations))
	for _, cit := range req.Citations {
		if notFound[cit] {
			items = append(items, resolveItem{Citation: cit, Found: false})
			continue
		}
		ri, ok := injByCit[cit]
		if !ok {
			// Shouldn't happen, but guard defensively.
			items = append(items, resolveItem{Citation: cit, Found: false})
			continue
		}
		mem, memOK := memByID[ri.memoryID]
		if !memOK {
			// Memory was deleted or out of scope.
			items = append(items, resolveItem{Citation: cit, Found: false})
			continue
		}
		item := resolveItem{
			Citation:   cit,
			Found:      true,
			Memory:     mem,
			Provenance: provByMemID[ri.memoryID],
			Rank:       ri.rank,
			Score:      ri.score,
		}
		// Split lane CSV into slice.
		if ri.lane != "" {
			item.Lanes = strings.Split(ri.lane, ",")
		}
		items = append(items, item)
	}

	respondJSON(w, http.StatusOK, resolveResponse{Items: items})
}
