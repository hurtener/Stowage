package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/hurtener/stowage/internal/config"
	"github.com/hurtener/stowage/internal/proactive"
	"github.com/hurtener/stowage/internal/store"
)

// suggestionsResponseJSON is the GET /v1/suggestions envelope. Shared shape with
// the SDK + MCP surfaces so all three are byte-identical (the D-067 parity bar).
type suggestionsResponseJSON struct {
	Suggestions []suggestionJSON `json:"suggestions"`
	Degraded    bool             `json:"degraded"`
}

type suggestionJSON struct {
	ID          string  `json:"id"`
	TriggerKind string  `json:"trigger_kind"`
	MemoryID    string  `json:"memory_id"`
	EpisodeID   string  `json:"episode_id,omitempty"`
	Title       string  `json:"title"`
	Content     string  `json:"content"`
	Score       float64 `json:"score"`
}

// resolveSuggestionResponseJSON is the POST /v1/suggestions/{id} envelope.
type resolveSuggestionResponseJSON struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

type resolveSuggestionRequestJSON struct {
	Action string `json:"action"` // "accept" | "dismiss"
}

// handleSuggestions implements GET /v1/suggestions?session_id=&query= (RFC §6d,
// D-087): the agent-initiated PULL. It resolves the scope's governance (profile
// default ⊕ stored override), runs the enabled trigger rules, scores candidates
// with the retrieval scorer, and surfaces the budgeted set — persisting them as
// pending offers (the feedback + dedupe record) and emitting suggestion.offered
// events. A repeated call within a session does not re-offer the same memory
// (dedupe), so the response is intentionally not idempotent: it returns what is
// newly worth surfacing this turn. Degrades to an empty+degraded envelope when the
// gateway-dependent similar_episode class cannot embed.
func (s *Server) handleSuggestions(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	scope, sessionID, err := s.resolveScope(r, identityArgs{User: q.Get("user"), Project: q.Get("project"), Session: q.Get("session_id")})
	if err != nil {
		respondScopeError(w, err)
		return
	}
	query := q.Get("query")

	cfg, err := proactive.Resolve(r.Context(), s.st.ScopeSettings(), scope, proactiveDefault(s.profile))
	if err != nil {
		s.log.ErrorContext(r.Context(), "api: suggestions: resolve governance failed", "err", err)
		respondJSON(w, http.StatusInternalServerError, errBody("suggestions governance failed"))
		return
	}

	offers, degraded, err := proactive.Evaluate(r.Context(), s.st, s.retriever, scope, sessionID, query, cfg, time.Now().UnixMilli())
	if errors.Is(err, proactive.ErrSessionRequired) {
		respondJSON(w, http.StatusBadRequest, errBody("session_id is required"))
		return
	}
	if err != nil {
		s.log.ErrorContext(r.Context(), "api: suggestions: evaluate failed", "err", err)
		respondJSON(w, http.StatusInternalServerError, errBody("suggestions evaluate failed"))
		return
	}
	respondJSON(w, http.StatusOK, suggestionsToJSON(offers, degraded))
}

// handleResolveSuggestion implements POST /v1/suggestions/{id} {action:accept|dismiss}
// (RFC §6d feedback tuning). accept/dismiss compare-and-swap the pending offer and
// feed the per-(scope,trigger_class) confidence multiplier. Resolving a
// non-pending (or absent) offer is a 404 — byte-identical across surfaces.
func (s *Server) handleResolveSuggestion(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	scope, _, err := s.resolveScope(r, identityArgs{User: q.Get("user"), Project: q.Get("project")})
	if err != nil {
		respondScopeError(w, err)
		return
	}
	id := r.PathValue("id")

	var body resolveSuggestionRequestJSON
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respondJSON(w, http.StatusBadRequest, errBody("invalid request body"))
		return
	}
	if body.Action != "accept" && body.Action != "dismiss" {
		respondJSON(w, http.StatusBadRequest, errBody("action must be accept or dismiss"))
		return
	}

	sug, err := proactive.ResolveOffer(r.Context(), s.st, scope, id, body.Action, time.Now().UnixMilli())
	if errors.Is(err, store.ErrNotPending) || errors.Is(err, store.ErrNotFound) {
		respondJSON(w, http.StatusNotFound, errBody("suggestion not found or already resolved"))
		return
	}
	if err != nil {
		s.log.ErrorContext(r.Context(), "api: suggestions: resolve failed", "err", err)
		respondJSON(w, http.StatusInternalServerError, errBody("suggestion resolve failed"))
		return
	}
	respondJSON(w, http.StatusOK, resolveSuggestionResponseJSON{ID: sug.ID, Status: sug.Status})
}

// proactiveDefault maps the profile's proactive config onto proactive.Config.
func proactiveDefault(profile string) proactive.Config {
	pc := config.ProactiveConfigForProfile(profile)
	return proactive.Config{Enabled: pc.Enabled, Threshold: pc.Threshold, Budget: pc.Budget, Classes: pc.Classes}
}

// suggestionsToJSON maps proactive offers onto the shared wire envelope.
func suggestionsToJSON(offers []proactive.Offer, degraded bool) suggestionsResponseJSON {
	out := suggestionsResponseJSON{Suggestions: make([]suggestionJSON, 0, len(offers)), Degraded: degraded}
	for _, o := range offers {
		out.Suggestions = append(out.Suggestions, suggestionJSON{
			ID: o.ID, TriggerKind: o.TriggerKind, MemoryID: o.MemoryID,
			EpisodeID: o.EpisodeID, Title: o.Title, Content: o.Content, Score: o.Score,
		})
	}
	return out
}
