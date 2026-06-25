package api

import (
	"net/http"

	"github.com/hurtener/stowage/internal/config"
	"github.com/hurtener/stowage/internal/playbook"
)

// playbookProvRefJSON is a compact provenance reference (mirrors the SDK
// PlaybookProvenanceRef and MCP PlaybookProvRef wire shapes).
type playbookProvRefJSON struct {
	RecordID  string `json:"record_id"`
	SpanStart int    `json:"span_start,omitempty"`
	SpanEnd   int    `json:"span_end,omitempty"`
}

// playbookItemJSON is one ranked memory in a section.
type playbookItemJSON struct {
	MemoryID   string                `json:"memory_id"`
	Kind       string                `json:"kind"`
	Content    string                `json:"content"`
	Score      float64               `json:"score"`
	Provenance []playbookProvRefJSON `json:"provenance,omitempty"`
}

// playbookSectionJSON groups the packed items of a single kind.
type playbookSectionJSON struct {
	Title string             `json:"title"`
	Kind  string             `json:"kind"`
	Items []playbookItemJSON `json:"items"`
}

// playbookBudgetJSON reports how the token budget was spent.
type playbookBudgetJSON struct {
	TokenBudget int `json:"token_budget"`
	TokensUsed  int `json:"tokens_used"`
	ItemsTotal  int `json:"items_total"`
	ItemsPacked int `json:"items_packed"`
}

// playbookResponseJSON is the GET /v1/playbook response envelope.
type playbookResponseJSON struct {
	Sections []playbookSectionJSON `json:"sections"`
	Budget   playbookBudgetJSON    `json:"budget"`
}

// handlePlaybook implements GET /v1/playbook.
//
// Returns the deterministic, sectioned, utility-ranked, budget-packed playbook
// for the authenticated scope (RFC §6a.3, D-072). LLM-free: it calls
// playbook.Assemble, which reads stored memories and the pure scoring functions
// only — no gateway. The token budget is profile-internal (D-034/D-042); the
// optional ?session_id= query param narrows assembly to one session.
func (s *Server) handlePlaybook(w http.ResponseWriter, r *http.Request) {
	scope := scopeFromRequest(r)

	pb, err := playbook.Assemble(r.Context(), s.st, scope, playbook.Options{
		SessionID:   r.URL.Query().Get("session_id"),
		TokenBudget: config.PlaybookBudgetForProfile(s.profile),
	})
	if err != nil {
		s.log.ErrorContext(r.Context(), "api: playbook: assemble failed", "err", err)
		respondJSON(w, http.StatusInternalServerError, errBody("playbook assembly failed"))
		return
	}

	respondJSON(w, http.StatusOK, playbookToJSON(pb))
}

// playbookToJSON maps the assembled playbook onto the wire envelope. Shared
// shape with the SDK + MCP surfaces so all three are byte-identical (AC-5).
func playbookToJSON(pb *playbook.Playbook) playbookResponseJSON {
	out := playbookResponseJSON{
		Sections: make([]playbookSectionJSON, 0, len(pb.Sections)),
		Budget: playbookBudgetJSON{
			TokenBudget: pb.Budget.TokenBudget,
			TokensUsed:  pb.Budget.TokensUsed,
			ItemsTotal:  pb.Budget.ItemsTotal,
			ItemsPacked: pb.Budget.ItemsPacked,
		},
	}
	for _, sec := range pb.Sections {
		js := playbookSectionJSON{Title: sec.Title, Kind: sec.Kind, Items: make([]playbookItemJSON, 0, len(sec.Items))}
		for _, it := range sec.Items {
			ji := playbookItemJSON{MemoryID: it.MemoryID, Kind: it.Kind, Content: it.Content, Score: it.Score}
			for _, p := range it.Provenance {
				ji.Provenance = append(ji.Provenance, playbookProvRefJSON{
					RecordID: p.RecordID, SpanStart: p.SpanStart, SpanEnd: p.SpanEnd,
				})
			}
			js.Items = append(js.Items, ji)
		}
		out.Sections = append(out.Sections, js)
	}
	return out
}
