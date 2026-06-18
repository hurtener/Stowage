package reflect

import (
	"fmt"
	"strings"
)

// ReflectionPromptVersion versions the reflection prompt skeleton. Bump when the
// text changes (golden files regenerate with UPDATE_GOLDEN=1).
const ReflectionPromptVersion = "1"

// reflectionSystemPrompt is the outcome-aware reflection instruction (ACE §6a.2).
// Distinct from the topic-extraction prompt: it distills transferable strategies
// and failure modes from a task trajectory + its outcome, not facts.
const reflectionSystemPrompt = `You are a reflection assistant for Stowage (reflection prompt v` + ReflectionPromptVersion + `).

You are given the trajectory of a single task — the ordered records of what an agent did — together with the task's final outcome (success or failure). Reflect on it the way ACE's Reflector does: extract the transferable lessons.

## What to produce

- kind "strategy": a reusable approach that WORKED and should be repeated ("when X, do Y because Z"). Prefer these on a success outcome.
- kind "failure_mode": a mistake or trap to AVOID next time ("doing X led to Y; instead Z"). Prefer these on a failure outcome.

## Instructions

1. Write each lesson as a complete, self-contained, transferable statement (no pronouns without antecedents; usable in a future, different task).
2. Provide 3–5 anticipated search queries an agent might use to find this lesson later.
3. Cite the source record(s) with approximate character spans (span_start inclusive, span_end exclusive).
4. Rate importance 1–5 (5 = high-impact, broadly reusable) and confidence 0.0–1.0.

## Constraints

- Distill LESSONS, do not restate raw facts or transcribe the conversation.
- Do not invent steps that did not happen.
- If the trajectory yields no transferable lesson, return an empty reflections array.
- Return a valid JSON object matching the response schema — no prose, no markdown fences.

## Record format

Each record is tagged:

  [record <ID>] role: <user|assistant|tool>
  <content>
`

// BuildReflectionPrompt assembles the (system, user) reflection prompt for one
// trajectory. Pure and deterministic — golden-tested.
func BuildReflectionPrompt(t Trajectory) (system, user string) {
	var b strings.Builder
	fmt.Fprintf(&b, "Task outcome: %s\n\n", outcomeLabel(t.Outcome))
	b.WriteString("Trajectory:\n")
	for _, r := range t.Records {
		fmt.Fprintf(&b, "[record %s] role: %s\n%s\n", r.ID, r.Role, strings.TrimSpace(r.Content))
	}
	return reflectionSystemPrompt, b.String()
}

func outcomeLabel(o string) string {
	switch o {
	case "success":
		return "SUCCESS"
	case "failure":
		return "FAILURE"
	default:
		return "UNKNOWN"
	}
}
