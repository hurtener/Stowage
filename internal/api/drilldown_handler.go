package api

import (
	"encoding/json"
	"net/http"

	"github.com/hurtener/stowage/internal/retrieval"
	"github.com/hurtener/stowage/internal/store"
)

// drilldownRequest is the wire format for POST /v1/drilldown.
// Exactly one of MemoryID or Citation must be set.
type drilldownRequest struct {
	MemoryID string `json:"memory_id"` // resolve by memory ID
	Citation string `json:"citation"`  // resolve by citation handle (injection ULID, D-051)
	// ProjectID/UserID scope the lookup to a sub-tenant identity (P3, D-125); empty =
	// tenant-wide. The store hard-isolates the memory/injection/record reads to this scope.
	ProjectID string `json:"project_id"`
	UserID    string `json:"user_id"`
}

// drilldownSpan is one provenance span in the response.
type drilldownSpan struct {
	RecordID   string `json:"record_id"`
	SpanStart  int    `json:"span_start"`
	SpanEnd    int    `json:"span_end"`
	Excerpt    string `json:"excerpt"` // content[SpanStart:SpanEnd], UTF-8 safe
	OccurredAt int64  `json:"occurred_at"`
	Role       string `json:"role"`
}

// drilldownResponse is the wire format for POST /v1/drilldown.
type drilldownResponse struct {
	MemoryID string          `json:"memory_id"`
	Spans    []drilldownSpan `json:"spans"`
}

// handleDrilldown implements POST /v1/drilldown.
//
// Accepts {memory_id} or {citation}. Resolves the target memory, fetches its
// provenance rows, and for each provenance row loads the verbatim record and
// returns a span excerpt (RFC P1, D-006, Phase 11).
//
// Spans are clamped to content bounds and UTF-8 safe (no mid-rune splits).
func (s *Server) handleDrilldown(w http.ResponseWriter, r *http.Request) {
	if !requireJSON(w, r) {
		return
	}

	var req drilldownRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		respondJSON(w, http.StatusBadRequest, errBody("decode: "+sanitizeDecodeErr(err)))
		return
	}

	if req.MemoryID == "" && req.Citation == "" {
		respondJSON(w, http.StatusBadRequest, errBody("one of memory_id or citation must be set"))
		return
	}
	if req.MemoryID != "" && req.Citation != "" {
		respondJSON(w, http.StatusBadRequest, errBody("only one of memory_id or citation may be set"))
		return
	}

	scope, _, err := s.resolveScope(r, identityArgs{Project: req.ProjectID, User: req.UserID})
	if err != nil {
		respondScopeError(w, err)
		return
	}

	memoryID := req.MemoryID

	// Resolve citation → memory_id via the injection store (D-051).
	if req.Citation != "" {
		inj, err := s.st.Injections().Get(r.Context(), scope, req.Citation)
		if err != nil {
			if isNotFound(err) {
				respondJSON(w, http.StatusNotFound, errBody("citation not found"))
				return
			}
			s.log.ErrorContext(r.Context(), "api: drilldown get injection", "err", err)
			respondJSON(w, http.StatusInternalServerError, errBody("internal error"))
			return
		}
		memoryID = inj.MemoryID
	}

	// Fetch provenance rows for the memory.
	junctions, err := s.st.Memories().GetJunctions(r.Context(), scope, memoryID)
	if err != nil {
		if isNotFound(err) {
			respondJSON(w, http.StatusNotFound, errBody("memory not found"))
			return
		}
		s.log.ErrorContext(r.Context(), "api: drilldown get junctions", "err", err)
		respondJSON(w, http.StatusInternalServerError, errBody("internal error"))
		return
	}

	if len(junctions.Provenance) == 0 {
		respondJSON(w, http.StatusOK, drilldownResponse{MemoryID: memoryID, Spans: []drilldownSpan{}})
		return
	}

	// Batch-fetch the referenced records.
	recordIDs := make([]string, 0, len(junctions.Provenance))
	seen := make(map[string]bool, len(junctions.Provenance))
	for _, p := range junctions.Provenance {
		if !seen[p.RecordID] {
			recordIDs = append(recordIDs, p.RecordID)
			seen[p.RecordID] = true
		}
	}

	records, err := s.st.Records().GetMany(r.Context(), scope, recordIDs)
	if err != nil {
		s.log.ErrorContext(r.Context(), "api: drilldown get records", "err", err)
		respondJSON(w, http.StatusInternalServerError, errBody("internal error"))
		return
	}

	// Index records by ID for fast lookup.
	recByID := make(map[string]store.Record, len(records))
	for _, rec := range records {
		recByID[rec.ID] = rec
	}

	spans := make([]drilldownSpan, 0, len(junctions.Provenance))
	for _, p := range junctions.Provenance {
		rec, ok := recByID[p.RecordID]
		if !ok {
			continue // record may have been deleted or out of scope
		}
		excerpt := retrieval.ClampExcerpt(rec.Content, p.SpanStart, p.SpanEnd)
		spans = append(spans, drilldownSpan{
			RecordID:   rec.ID,
			SpanStart:  p.SpanStart,
			SpanEnd:    p.SpanEnd,
			Excerpt:    excerpt,
			OccurredAt: rec.OccurredAt,
			Role:       rec.Role,
		})
	}

	respondJSON(w, http.StatusOK, drilldownResponse{MemoryID: memoryID, Spans: spans})
}
