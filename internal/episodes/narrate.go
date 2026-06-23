package episodes

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hurtener/stowage/internal/gateway"
	"github.com/hurtener/stowage/internal/store"
)

// NarrativeSchemaVersion tags the narrative response schema.
const NarrativeSchemaVersion = "1"

// narrateMaxTokens gives reasoning headroom (thinking models — the 2026-06-12
// lesson); narratives are short but the budget must not truncate the JSON.
const narrateMaxTokens = 4096

// narrativeSchema constrains the narration response (CLAUDE.md §10 / D-040 —
// schema-constrained, no free-text JSON parsing).
var narrativeSchema = json.RawMessage(`{
  "$schema": "http://json-schema.org/draft-07/schema#",
  "title": "EpisodeNarrative",
  "type": "object",
  "required": ["title", "narrative"],
  "additionalProperties": false,
  "properties": {
    "title":     { "type": "string", "description": "A short title for the episode." },
    "narrative": { "type": "string", "description": "The concrete path of what happened and the decisions taken, grounded in the records." }
  }
}`)

const narrativeSystemPrompt = `You are constructing a narrative memory for an episode in Stowage (narrative schema v` + NarrativeSchemaVersion + `).

Read the episode's records (in order) and write the CONCRETE path of what happened and the decisions taken — not "we discussed X", but the actual sequence and outcomes. Be specific and grounded ONLY in the records; do not invent.

## Output
- title: a short, specific episode title.
- narrative: a few sentences telling the episode's story (actions, decisions, outcome).

Return a valid JSON object matching the response schema — no prose, no markdown fences.

## Record format
Each record is tagged:
  [record <ID>] role: <user|assistant|tool>
  <content>
`

// Narrative is the narration result for an episode.
type Narrative struct {
	Title     string
	Narrative string
}

type narrativeOut struct {
	Title     string `json:"title"`
	Narrative string `json:"narrative"`
}

// BuildNarrativePrompt assembles the (system, user) narration prompt for an
// episode's records. Pure and deterministic — golden-tested.
func BuildNarrativePrompt(records []store.Record) (system, user string) {
	var b strings.Builder
	b.WriteString("Episode records:\n")
	for _, r := range records {
		fmt.Fprintf(&b, "[record %s] role: %s\n%s\n", r.ID, r.Role, strings.TrimSpace(r.Content))
	}
	return narrativeSystemPrompt, b.String()
}

// Narrate constructs an episode narrative via the gateway (schema-constrained,
// §10; routed through gateway.Gateway, P5). Returns the title + narrative text.
func Narrate(ctx context.Context, gw gateway.Gateway, records []store.Record) (Narrative, error) {
	if len(records) == 0 {
		return Narrative{}, fmt.Errorf("episodes: narrate: no records")
	}
	system, user := BuildNarrativePrompt(records)
	resp, err := gw.Complete(ctx, gateway.CompleteRequest{
		System:      system,
		Messages:    []gateway.Message{{Role: "user", Content: user}},
		Schema:      narrativeSchema,
		MaxTokens:   narrateMaxTokens,
		Temperature: 0.0,
	})
	if err != nil {
		return Narrative{}, fmt.Errorf("episodes: narrate complete: %w", err)
	}
	var no narrativeOut
	if err := json.Unmarshal(resp.JSON, &no); err != nil {
		return Narrative{}, fmt.Errorf("episodes: narrate decode: %w", err)
	}
	if no.Narrative == "" {
		return Narrative{}, fmt.Errorf("episodes: narrate: empty narrative")
	}
	return Narrative(no), nil
}
