package api

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/retrieval"
	"github.com/hurtener/stowage/internal/scoring"
	"github.com/hurtener/stowage/internal/store"
)

// retrieveRequest is the wire format for POST /v1/retrieve (envelope v1, Phase 11).
type retrieveRequest struct {
	Query        string   `json:"query"`
	Limit        int      `json:"limit"`
	From         int64    `json:"from"`  // unix millis; 0 = unbounded
	Until        int64    `json:"until"` // unix millis; 0 = unbounded
	Kinds        []string `json:"kinds"`
	IncludeLanes bool     `json:"include_lanes"`
	SessionID    string   `json:"session_id"`  // used for cooldown + scope affinity (Phase 10)
	Debug        bool     `json:"debug"`       // if true, include per-item scoring breakdowns (Phase 10)
	ResponseID   string   `json:"response_id"` // caller-supplied; generated when absent (D-051)
	Profile      string   `json:"profile"`     // "precise"|"balanced"|"broad"; default "balanced"
}

// retrieveBreakdown is the wire format for a per-item scoring breakdown.
type retrieveBreakdown struct {
	UseBoost         float64 `json:"use_boost"`
	NoisePenalty     float64 `json:"noise_penalty"`
	PrecisionFactor  float64 `json:"precision_factor"`
	ExplorationBonus float64 `json:"exploration_bonus"`
	DecayFactor      float64 `json:"decay_factor"`
	TrustMultiplier  float64 `json:"trust_multiplier"`
	ScopeAffinity    float64 `json:"scope_affinity"`
	TemporalBoost    float64 `json:"temporal_boost"`
	HubDampening     float64 `json:"hub_dampening"`
	Cooldown         float64 `json:"cooldown"`
	ImportanceMult   float64 `json:"importance_mult"`
	FinalScore       float64 `json:"final_score"`
}

// retrieveConflict is a pair of memory IDs connected by a contradicts link.
type retrieveConflict struct {
	A string `json:"a"`
	B string `json:"b"`
}

// retrieveSupport is the per-response evidence summary (RFC §4.2.5).
type retrieveSupport struct {
	Strength  string             `json:"strength"`
	TopScore  float64            `json:"top_score"`
	Conflicts []retrieveConflict `json:"conflicts,omitempty"`
}

// retrieveMemoryItem is the wire format for one retrieval result (envelope v1).
type retrieveMemoryItem struct {
	ID        string             `json:"id"`
	Kind      string             `json:"kind"`
	Content   string             `json:"content"`
	Context   string             `json:"context,omitempty"`
	Score     float64            `json:"score"`
	Citation  string             `json:"citation"` // injection ULID = citation handle (D-051)
	Lanes     []string           `json:"lanes,omitempty"`
	Breakdown *retrieveBreakdown `json:"breakdown,omitempty"` // present when debug:true
	// Stale marks a superseded value surfaced for dual-visibility (D-105, §6c); prefer
	// the current value, SupersededBy links to its successor.
	Stale        bool   `json:"stale,omitempty"`
	SupersededBy string `json:"superseded_by,omitempty"`
	// OccurredAt is the assertion (conversation) date of the memory in unix millis, so a
	// reader can do temporal reasoning and date-resolve stale values (D-109). 0 when unknown.
	OccurredAt int64 `json:"occurred_at,omitempty"`
}

// retrieveResponse is the wire format for POST /v1/retrieve (envelope v1).
type retrieveResponse struct {
	ResponseID     string               `json:"response_id"` // echoed or generated ULID (D-051)
	Items          []retrieveMemoryItem `json:"items"`
	Support        retrieveSupport      `json:"support"`
	Degraded       bool                 `json:"degraded"`
	DegradedRerank bool                 `json:"degraded_rerank,omitempty"` // true when rerank failed; Phase-10 order preserved (Phase 12)
	CacheHit       bool                 `json:"cache_hit,omitempty"`       // true when served from the hot–warm cache (Phase 12)
	API            string               `json:"api"`                       // "v1"
}

// handleRetrieve implements POST /v1/retrieve.
//
// The handler authenticates the caller (any valid key), extracts the tenant
// scope, calls retrieval.Retriever.Retrieve, and returns the fused + scored
// results. When the vector lane is unavailable the response still returns 200
// with degraded:true (D-036).
//
// Phase 10 additions:
//   - session_id in request enables write-echo cooldown detection.
//   - debug:true in request adds per-item scoring breakdowns.
//   - support block (strength, top_score, conflicts) is always present.
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

	// Validate profile (D-034: knob ships with validation).
	validProfiles := map[string]bool{"": true, "precise": true, "balanced": true, "broad": true}
	if !validProfiles[req.Profile] {
		respondJSON(w, http.StatusBadRequest, errBody(fmt.Sprintf("unknown profile %q (want precise|balanced|broad)", req.Profile)))
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
		SessionID:    req.SessionID,
		Debug:        req.Debug,
		ResponseID:   req.ResponseID,
		Profile:      req.Profile,
	})
	if err != nil {
		s.log.ErrorContext(r.Context(), "api: retrieve failed", "err", err)
		respondJSON(w, http.StatusInternalServerError, errBody("retrieve error"))
		return
	}

	items := make([]retrieveMemoryItem, 0, len(resp.Items))
	for _, item := range resp.Items {
		ri := retrieveMemoryItem{
			ID:       item.Memory.ID,
			Kind:     item.Memory.Kind,
			Content:  item.Memory.Content,
			Context:  item.Memory.Context,
			Score:    item.Score,
			Citation: item.Citation,
		}
		if item.Stale {
			ri.Stale = true
			ri.SupersededBy = item.Memory.SupersededByID
		}
		ri.OccurredAt = item.Memory.ValidFrom
		if req.IncludeLanes {
			ri.Lanes = item.Lanes
		}
		if req.Debug && item.Breakdown != nil {
			ri.Breakdown = breakdownToWire(item.Breakdown)
		}
		items = append(items, ri)
	}

	// Map support summary to wire format.
	sup := retrieveSupport{
		Strength: resp.Support.Strength,
		TopScore: resp.Support.TopScore,
	}
	for _, c := range resp.Support.Conflicts {
		sup.Conflicts = append(sup.Conflicts, retrieveConflict{A: c.A, B: c.B})
	}

	respondJSON(w, http.StatusOK, retrieveResponse{
		ResponseID:     resp.ResponseID,
		Items:          items,
		Support:        sup,
		Degraded:       resp.Degraded,
		DegradedRerank: resp.DegradedRerank,
		CacheHit:       resp.CacheHit,
		API:            resp.API,
	})
}

// breakdownToWire converts a scoring.Breakdown to the API wire format.
func breakdownToWire(bd *scoring.Breakdown) *retrieveBreakdown {
	if bd == nil {
		return nil
	}
	return &retrieveBreakdown{
		UseBoost:         bd.UseBoost,
		NoisePenalty:     bd.NoisePenalty,
		PrecisionFactor:  bd.PrecisionFactor,
		ExplorationBonus: bd.ExplorationBonus,
		DecayFactor:      bd.DecayFactor,
		TrustMultiplier:  bd.TrustMultiplier,
		ScopeAffinity:    bd.ScopeAffinity,
		TemporalBoost:    bd.TemporalBoost,
		HubDampening:     bd.HubDampening,
		Cooldown:         bd.Cooldown,
		ImportanceMult:   bd.ImportanceMult,
		FinalScore:       bd.FinalScore,
	}
}
