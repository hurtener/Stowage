package pipeline

import (
	"fmt"
	"strings"

	"github.com/hurtener/stowage/internal/store"
	"github.com/hurtener/stowage/internal/tokenize"
)

// PromptTemplateVersion is the version of the extraction prompt template.
// Increment when the template text changes; golden files must be regenerated
// with UPDATE_GOLDEN=1.
const PromptTemplateVersion = "4"

// systemPromptTemplate is the versioned system-prompt skeleton.
// The literal "{topics}" placeholder is replaced by BuildPrompt.
const systemPromptTemplate = `You are a memory extraction assistant for Stowage (prompt template v` + PromptTemplateVersion + `).

Your task: read the conversation transcript that follows and extract memorable information that matches the active topics listed below.

## Active topics

{topics}

## Instructions

For each piece of memorable information you identify:
1. Choose the most fitting kind: fact | preference | decision | gotcha | pattern | task | narrative
2. Write the content as a complete, self-contained statement (avoid pronouns without clear antecedents)
3. PRESERVE every quantitative qualifier, unit, scope, and condition exactly as stated — dropping one changes the fact. Write "45 minutes each way" (not "about 45 minutes"), "$5,850 gross" (not "$5,850"), "12,000 steps per day" (not "12,000 steps"), "120 stars within 12 months".
4. When the user corrects or updates a value, record ONLY the current value as a plain present-tense assertion. Do NOT narrate the change. Write "The user has been using the Fitbit Charge 3 for 9 months" — NEVER "changed from 6 months to 9 months", "first said 6, later 9", or "was X, now Y". A memory must not contain two competing values for the same fact; the superseded value is forgotten elsewhere, not carried inside the new memory.
5. Populate "context" with the disambiguating conversational frame — what was being discussed, and any comparison, condition, or timeframe that makes the value interpretable on its own later (e.g. "commute time discussed while choosing audiobooks vs podcasts").
6. Provide 3–5 anticipated search queries a user might use to find this memory later
7. Cite the source record(s) with approximate character spans (span_start inclusive, span_end exclusive)
8. Rate importance 1–5 (1 = trivial background noise, 5 = business-critical or safety-relevant) and confidence 0.0–1.0
9. Set "topics" to the key(s) of the active topics above that this candidate pertains to — use the EXACT topic keys (the text before the colon); [] if none clearly apply

## Constraints

- Extract ONLY information that clearly matches one of the active topics above
- Do not invent information or infer beyond what the conversation explicitly states
- A candidate with no clear topic match must be omitted
- Return a valid JSON object matching the response schema — no prose, no markdown fences

## Record format

Each record in the transcript is tagged:

  [record <ID>] role: <user|assistant|tool>
  <content>
`

// PromptResult holds the assembled extraction prompt ready for the gateway.
type PromptResult struct {
	// SystemPrompt is the assembled system message (template + topics section).
	SystemPrompt string
	// UserContent is the transcript of records, formatted as the user-turn message.
	UserContent string
	// Truncated is true when the transcript was clamped to fit the token budget
	// (oldest records were dropped).
	Truncated bool
}

// tokenBudgetForProfile returns the max transcript token budget for the given
// profile. Not a top-level config knob (D-034 guardrail); profile-level constant.
//
//   - assistant   → 8 000 tokens
//   - coding-agent / fleet → 12 000 tokens
func tokenBudgetForProfile(profile string) int {
	switch profile {
	case "coding-agent", "fleet":
		return 12000
	default: // "assistant" and fallback
		return 8000
	}
}

// roughTokens estimates the token count of s using the 4-chars ≈ 1 token
// heuristic. Used for transcript clamping — not charged to the provider.
func roughTokens(s string) int { return tokenize.Estimate(s) }

// BuildPrompt assembles the extraction prompt from active topic descriptions
// and the hydrated records for the flush.
//
// topicLines are "key: description" strings, one per active topic.
// tokenBudget is the max transcript token count (roughTokens units).
//
// Records are included oldest-first (natural order). When the total exceeds the
// budget, the oldest are dropped until it fits; Truncated is set in the result.
func BuildPrompt(topicLines []string, records []store.Record, tokenBudget int) PromptResult {
	// Assemble the topics section (one bullet per line).
	var topicBlock strings.Builder
	for _, line := range topicLines {
		topicBlock.WriteString("- ")
		topicBlock.WriteString(line)
		topicBlock.WriteString("\n")
	}
	topicsStr := strings.TrimSuffix(topicBlock.String(), "\n")
	if topicsStr == "" {
		topicsStr = "(none)"
	}
	system := strings.ReplaceAll(systemPromptTemplate, "{topics}", topicsStr)

	// Build per-record transcript blocks.
	type block struct {
		text   string
		tokens int
	}
	blocks := make([]block, len(records))
	for i, rec := range records {
		text := fmt.Sprintf("[record %s] role: %s\n%s\n", rec.ID, rec.Role, rec.Content)
		blocks[i] = block{text: text, tokens: roughTokens(text)}
	}

	// Sum total tokens.
	total := 0
	for _, b := range blocks {
		total += b.tokens
	}

	// Clamp oldest-first: drop oldest blocks until total <= budget.
	truncated := false
	start := 0
	for total > tokenBudget && start < len(blocks) {
		total -= blocks[start].tokens
		start++
		truncated = true
	}

	var sb strings.Builder
	for _, b := range blocks[start:] {
		sb.WriteString(b.text)
	}

	return PromptResult{
		SystemPrompt: system,
		UserContent:  sb.String(),
		Truncated:    truncated,
	}
}
