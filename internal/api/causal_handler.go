package api

import (
	"net/http"

	"github.com/hurtener/stowage/internal/causal"
)

// causalNodeJSON is one memory in the causal graph (byte-identical to SDK + MCP).
type causalNodeJSON struct {
	MemoryID   string              `json:"memory_id"`
	Kind       string              `json:"kind"`
	Content    string              `json:"content"`
	Context    string              `json:"context,omitempty"`
	EpisodeID  string              `json:"episode_id,omitempty"`
	Provenance []causalProvRefJSON `json:"provenance,omitempty"`
}

type causalProvRefJSON struct {
	RecordID  string `json:"record_id"`
	SpanStart int    `json:"span_start,omitempty"`
	SpanEnd   int    `json:"span_end,omitempty"`
}

type causalEdgeJSON struct {
	From       string  `json:"from"`
	To         string  `json:"to"`
	Type       string  `json:"type"`
	Confidence float64 `json:"confidence"`
}

type causalResponseJSON struct {
	Root      string           `json:"root"`
	Nodes     []causalNodeJSON `json:"nodes"`
	Edges     []causalEdgeJSON `json:"edges"`
	Truncated bool             `json:"truncated,omitempty"`
}

// handleCausal implements GET /v1/causal (RFC §5.6/§6b, D-083): the deterministic,
// gateway-free why-traversal of the caused_by/led_to graph from `memory_id`.
// `direction` ∈ {backward (default), forward, both}; `depth` bounds the hops.
func (s *Server) handleCausal(w http.ResponseWriter, r *http.Request) {
	scope := scopeFromRequest(r)
	q := r.URL.Query()

	memID := q.Get("memory_id")
	if memID == "" {
		respondJSON(w, http.StatusBadRequest, errBody("memory_id is required"))
		return
	}
	dir := q.Get("direction")
	switch dir {
	case "", "backward", "forward", "both":
		// valid (empty defaults to backward in the core)
	default:
		respondJSON(w, http.StatusBadRequest, errBody("direction must be backward, forward, or both"))
		return
	}
	g, err := causal.Traverse(r.Context(), s.st, scope, memID, causal.Direction(dir), atoiDefault(q.Get("depth"), 0))
	if err != nil {
		s.log.ErrorContext(r.Context(), "api: causal: traverse failed", "err", err)
		respondJSON(w, http.StatusInternalServerError, errBody("causal traverse failed"))
		return
	}
	respondJSON(w, http.StatusOK, causalGraphToJSON(g))
}

func causalGraphToJSON(g causal.Graph) causalResponseJSON {
	out := causalResponseJSON{Root: g.Root, Truncated: g.Truncated,
		Nodes: make([]causalNodeJSON, 0, len(g.Nodes)), Edges: make([]causalEdgeJSON, 0, len(g.Edges))}
	for _, n := range g.Nodes {
		cn := causalNodeJSON{MemoryID: n.MemoryID, Kind: n.Kind, Content: n.Content, Context: n.Context, EpisodeID: n.EpisodeID}
		for _, p := range n.Provenance {
			cn.Provenance = append(cn.Provenance, causalProvRefJSON{RecordID: p.RecordID, SpanStart: p.SpanStart, SpanEnd: p.SpanEnd})
		}
		out.Nodes = append(out.Nodes, cn)
	}
	for _, e := range g.Edges {
		out.Edges = append(out.Edges, causalEdgeJSON{From: e.From, To: e.To, Type: e.Type, Confidence: e.Confidence})
	}
	return out
}
