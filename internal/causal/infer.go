// Package causal implements Phase-24 causal links (RFC §5.6, §6b, D-083): an
// inference step that proposes confidence-scored led_to edges between an episode's
// decision memories (schema-constrained gateway call, P5/D-040), and a deterministic,
// gateway-free traversal of the caused_by/led_to graph ("why did X lead to Y").
//
// infer.go is the inference half (the only gateway-touching file). traverse.go is the
// gateway-free read half — keep them split so the no-gateway lint on traversal holds.
package causal

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hurtener/stowage/internal/gateway"
)

// CausalSchemaVersion tags the inference response schema.
const CausalSchemaVersion = "1"

// inferMaxTokens gives reasoning headroom (thinking models); the JSON is small but
// the budget must not truncate it.
const inferMaxTokens = 4096

// DecisionKinds are the causal-actor memory kinds the inference considers — acts and
// outcomes, not state. Facts/preferences/narratives are excluded (they are not causes
// or effects in the §6b sense). The narration sweep gathers candidates of these kinds.
var DecisionKinds = []string{"decision", "task", "gotcha", "pattern", "strategy", "failure_mode"}

// Candidate is one decision memory offered to the inference, numbered by slice index.
type Candidate struct {
	ID      string
	Kind    string
	Content string
}

// ProposedLink is one gateway-proposed causal edge, cause→effect, by candidate index.
type ProposedLink struct {
	FromIdx    int     `json:"from_idx"`
	ToIdx      int     `json:"to_idx"`
	Confidence float64 `json:"confidence"`
	Reason     string  `json:"reason"`
}

// causalSchema constrains the inference response (§10 / D-040 — schema-constrained,
// no free-text JSON parsing).
var causalSchema = json.RawMessage(`{
  "$schema": "http://json-schema.org/draft-07/schema#",
  "title": "CausalLinks",
  "type": "object",
  "required": ["links"],
  "additionalProperties": false,
  "properties": {
    "links": {
      "type": "array",
      "items": {
        "type": "object",
        "required": ["from_idx", "to_idx", "confidence", "reason"],
        "additionalProperties": false,
        "properties": {
          "from_idx":   { "type": "integer", "description": "Index of the CAUSE decision (0-based, into the provided decision list)." },
          "to_idx":     { "type": "integer", "description": "Index of the EFFECT decision the cause led to (0-based)." },
          "confidence": { "type": "number", "description": "0.0 to 1.0 — how strongly the narrative supports this causal link." },
          "reason":     { "type": "string", "description": "A short rationale grounded in the narrative." }
        }
      }
    }
  }
}`)

const inferSystemPrompt = `You are inferring CAUSAL links between the decisions of one episode in Stowage (causal schema v` + CausalSchemaVersion + `).

You are given the episode's NARRATIVE (the concrete path of what happened) and a numbered list of DECISION memories from that episode. Propose directed "led_to" edges: from_idx is the CAUSE, to_idx is the EFFECT it led to.

Rules:
- Only propose an edge when the NARRATIVE concretely supports that one decision led to another. Do not invent causality from surface similarity.
- from_idx and to_idx must be distinct indices that appear in the list.
- confidence reflects how strongly the narrative supports the link (0–1).
- If nothing is clearly causal, return an empty links array.

Return a valid JSON object matching the response schema — no prose, no markdown fences.

## Decision format
  [N] (kind) content`

type inferOut struct {
	Links []ProposedLink `json:"links"`
}

// BuildInferencePrompt assembles the (system, user) inference prompt. Pure and
// deterministic — golden-tested.
func BuildInferencePrompt(narrative string, cands []Candidate) (system, user string) {
	var b strings.Builder
	b.WriteString("Episode narrative:\n")
	b.WriteString(strings.TrimSpace(narrative))
	b.WriteString("\n\nDecisions:\n")
	for i, c := range cands {
		fmt.Fprintf(&b, "[%d] (%s) %s\n", i, c.Kind, strings.TrimSpace(c.Content))
	}
	return inferSystemPrompt, b.String()
}

// Infer proposes led_to edges among an episode's decisions given its narrative
// (schema-constrained Complete, P5/D-040). It returns the RAW proposals; the caller
// confidence-gates, drops self/out-of-range indices, and maps indices→memory IDs.
// A gateway error is returned (the caller narrates without links — best-effort).
// Fewer than two candidates ⇒ no call, no edges.
func Infer(ctx context.Context, gw gateway.Gateway, narrative string, cands []Candidate) ([]ProposedLink, error) {
	if gw == nil {
		return nil, fmt.Errorf("causal: infer: nil gateway")
	}
	if len(cands) < 2 {
		return nil, nil
	}
	system, user := BuildInferencePrompt(narrative, cands)
	resp, err := gw.Complete(ctx, gateway.CompleteRequest{
		System:      system,
		Messages:    []gateway.Message{{Role: "user", Content: user}},
		Schema:      causalSchema,
		MaxTokens:   inferMaxTokens,
		Temperature: 0.0,
	})
	if err != nil {
		return nil, fmt.Errorf("causal: infer complete: %w", err)
	}
	var out inferOut
	if err := json.Unmarshal(resp.JSON, &out); err != nil {
		return nil, fmt.Errorf("causal: infer decode: %w", err)
	}
	return out.Links, nil
}

// GateProposals filters raw proposals to those meeting minConfidence with distinct,
// in-range indices, and dedupes (from,to) pairs (keeping the first occurrence in the
// model's returned order). It is the deterministic, gateway-free gate the lifecycle
// caller applies before persisting. n is len(candidates).
func GateProposals(props []ProposedLink, n int, minConfidence float64) []ProposedLink {
	out := make([]ProposedLink, 0, len(props))
	seen := make(map[[2]int]struct{}, len(props))
	for _, p := range props {
		if p.Confidence < minConfidence {
			continue
		}
		if p.FromIdx < 0 || p.ToIdx < 0 || p.FromIdx >= n || p.ToIdx >= n || p.FromIdx == p.ToIdx {
			continue
		}
		key := [2]int{p.FromIdx, p.ToIdx}
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, p)
	}
	return out
}
