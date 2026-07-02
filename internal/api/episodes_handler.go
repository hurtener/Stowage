package api

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/hurtener/stowage/internal/episodes"
	"github.com/hurtener/stowage/internal/store"
)

// episodeViewJSON is one episode + its narrative (mirrors the SDK + MCP shapes).
type episodeViewJSON struct {
	ID                string  `json:"id"`
	SessionID         string  `json:"session_id"`
	Title             string  `json:"title"`
	Status            string  `json:"status"`
	Outcome           string  `json:"outcome,omitempty"`
	StartedAt         int64   `json:"started_at"`
	EndedAt           int64   `json:"ended_at"`
	NarrativeMemoryID string  `json:"narrative_memory_id,omitempty"`
	Narrative         string  `json:"narrative,omitempty"`
	Score             float64 `json:"score,omitempty"`
}

// episodesResponseJSON is the GET /v1/episodes envelope.
type episodesResponseJSON struct {
	Episodes   []episodeViewJSON `json:"episodes"`
	NextCursor string            `json:"next_cursor,omitempty"`
	Degraded   bool              `json:"degraded,omitempty"`
}

// handleEpisodes implements GET /v1/episodes (RFC §6b, D-080): the deterministic,
// LLM-free episodic-retrieval read. `?id=` returns one episode; otherwise a
// most-recent-first list, optionally narrowed by `?session_id=` and the
// `?from=&until=` time window (the cross-episode structured summary). Tenant scope
// from the auth key (a tenant query matches the tenant's episodes).
func (s *Server) handleEpisodes(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	scope, effSession, err := s.resolveScope(r, identityArgs{Project: q.Get("project_id"), User: q.Get("user_id"), Session: q.Get("session_id")})
	if err != nil {
		respondScopeError(w, err)
		return
	}

	if id := q.Get("id"); id != "" {
		v, err := episodes.Get(r.Context(), s.st, scope, id)
		if errors.Is(err, store.ErrNotFound) {
			// `id` is a filter on /v1/episodes (not a REST resource path), so a miss
			// returns an empty list — byte-identical to the embedded SDK + MCP
			// surfaces (the D-067 parity bar; not a 404). The list path returns the
			// same empty envelope.
			respondJSON(w, http.StatusOK, episodesResponseJSON{Episodes: []episodeViewJSON{}})
			return
		}
		if err != nil {
			s.log.ErrorContext(r.Context(), "api: episodes: get failed", "err", err)
			respondJSON(w, http.StatusInternalServerError, errBody("episode get failed"))
			return
		}
		respondJSON(w, http.StatusOK, episodesResponseJSON{Episodes: []episodeViewJSON{episodeToJSON(*v)}})
		return
	}

	// similar_to: vector-rank the scope's episodes by narrative similarity (§6b,
	// D-082). Degrades to an empty+degraded envelope when the gateway is down.
	if query := q.Get("similar_to"); query != "" {
		if s.retriever == nil {
			respondJSON(w, http.StatusOK, episodesResponseJSON{Episodes: []episodeViewJSON{}, Degraded: true})
			return
		}
		views, degraded, err := episodes.Similar(r.Context(), s.st, s.retriever, scope, query, atoiDefault(q.Get("k"), 0))
		if err != nil {
			s.log.ErrorContext(r.Context(), "api: episodes: similar failed", "err", err)
			respondJSON(w, http.StatusInternalServerError, errBody("episode similar failed"))
			return
		}
		out := episodesResponseJSON{Episodes: make([]episodeViewJSON, 0, len(views)), Degraded: degraded}
		for _, v := range views {
			out.Episodes = append(out.Episodes, episodeToJSON(v))
		}
		respondJSON(w, http.StatusOK, out)
		return
	}

	// arc_of: return the cross-session arc of an episode (§6b threading, D-081).
	if seed := q.Get("arc_of"); seed != "" {
		views, err := episodes.Arc(r.Context(), s.st, scope, seed)
		if err != nil {
			s.log.ErrorContext(r.Context(), "api: episodes: arc failed", "err", err)
			respondJSON(w, http.StatusInternalServerError, errBody("episode arc failed"))
			return
		}
		out := episodesResponseJSON{Episodes: make([]episodeViewJSON, 0, len(views))}
		for _, v := range views {
			out.Episodes = append(out.Episodes, episodeToJSON(v))
		}
		respondJSON(w, http.StatusOK, out)
		return
	}

	res, err := episodes.List(r.Context(), s.st, scope, episodes.ListOptions{
		Limit:  atoiDefault(q.Get("limit"), 0),
		Cursor: q.Get("cursor"),
		// Session-REPLACE (D-137/D-150): the effective session (claim > arg —
		// HTTP has no _meta, D-140), never Scope.Session.
		SessionID: effSession,
		From:      atoi64(q.Get("from")),
		Until:     atoi64(q.Get("until")),
	})
	if err != nil {
		s.log.ErrorContext(r.Context(), "api: episodes: list failed", "err", err)
		respondJSON(w, http.StatusInternalServerError, errBody("episode list failed"))
		return
	}
	respondJSON(w, http.StatusOK, episodesToJSON(res))
}

func episodeToJSON(v episodes.EpisodeView) episodeViewJSON {
	return episodeViewJSON{
		ID: v.ID, SessionID: v.SessionID, Title: v.Title, Status: v.Status, Outcome: v.Outcome,
		StartedAt: v.StartedAt, EndedAt: v.EndedAt, NarrativeMemoryID: v.NarrativeMemoryID, Narrative: v.Narrative,
		Score: v.Score,
	}
}

func episodesToJSON(res episodes.ListResult) episodesResponseJSON {
	out := episodesResponseJSON{Episodes: make([]episodeViewJSON, 0, len(res.Episodes)), NextCursor: res.NextCursor}
	for _, v := range res.Episodes {
		out.Episodes = append(out.Episodes, episodeToJSON(v))
	}
	return out
}

func atoiDefault(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}

func atoi64(s string) int64 {
	if s == "" {
		return 0
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0
	}
	return n
}
