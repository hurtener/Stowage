package api

import (
	"errors"
	"net/http"

	"github.com/hurtener/stowage/internal/retrieval"
	"github.com/hurtener/stowage/internal/store"
)

// browseResponseJSON is the GET /v1/memories envelope.
type browseResponseJSON struct {
	Memories   []memoryJSON `json:"memories"`
	NextCursor string       `json:"next_cursor,omitempty"`
}

// handleBrowseMemories implements GET /v1/memories (ae5, D-143): the
// deterministic, gateway-free scoped walk over the caller's memories.
// `?mode=` selects the walk axis — "recent" (default, most-recent-first via
// the new Store.ListByScopeRecent) or "superseded" (oldest-first — reuses the
// EXISTING Store.ListByStatus query, H4; the ordering asymmetry is
// deliberate, D-143). `?limit=&cursor=` paginate; `?project_id=&user_id=`
// narrow the scope (P3, D-125). Thin caller over retrieval.Browse — the one
// core (D-067/D-073).
func (s *Server) handleBrowseMemories(w http.ResponseWriter, r *http.Request) {
	scope, err := s.scopeFromRequest(r)
	if err != nil {
		respondScopeError(w, err)
		return
	}
	q := r.URL.Query()

	mode, err := retrieval.ParseBrowseMode(q.Get("mode"))
	if err != nil {
		respondJSON(w, http.StatusBadRequest, errBody(err.Error()))
		return
	}

	res, err := retrieval.Browse(r.Context(), s.st, scope, retrieval.BrowseOptions{
		Mode:         mode,
		Limit:        atoiDefault(q.Get("limit"), 0),
		Cursor:       q.Get("cursor"),
		DefaultLimit: s.browseDefaultLimit,
	})
	if err != nil {
		switch {
		case errors.Is(err, store.ErrBadCursor):
			respondJSON(w, http.StatusBadRequest, errBody("invalid cursor"))
		case errors.Is(err, store.ErrScopeRequired):
			respondJSON(w, http.StatusBadRequest, errBody("scope required"))
		default:
			s.log.ErrorContext(r.Context(), "api: browse memories failed", "err", err)
			respondJSON(w, http.StatusInternalServerError, errBody("browse failed"))
		}
		return
	}

	out := browseResponseJSON{Memories: make([]memoryJSON, 0, len(res.Memories)), NextCursor: res.NextCursor}
	for i := range res.Memories {
		out.Memories = append(out.Memories, memoryToJSON(&res.Memories[i]))
	}
	respondJSON(w, http.StatusOK, out)
}
