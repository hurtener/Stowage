// Package trust implements the Phase-25 trust safeguards (RFC §6c, D-084): claim
// verification (a schema-constrained gateway entailment check that a claim is
// supported by its cited memories) and the review queue (uncited agent assertions
// park as pending_review and are approved/rejected reversibly).
//
// verify.go is the only gateway-touching file; review.go is gateway-free.
package trust

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/hurtener/stowage/internal/gateway"
	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/store"
)

// VerifySchemaVersion tags the verification response schema.
const VerifySchemaVersion = "1"

const verifyMaxTokens = 2048

// Verdict values.
const (
	VerdictEntailed    = "entailed"
	VerdictNotEntailed = "not_entailed"
	VerdictUnclear     = "unclear"
)

// CitedMemory is one resolved citation the claim is checked against.
type CitedMemory struct {
	ID      string
	Content string
}

// Verdict is the verification result.
type Verdict struct {
	Verdict     string  // entailed | not_entailed | unclear
	Confidence  float64 // 0–1
	Explanation string
	Degraded    bool // gateway unreachable ⇒ unclear+degraded, no error (D-036)
}

// verifySchema constrains the entailment response (§10 / D-040 — no free-text JSON).
var verifySchema = json.RawMessage(`{
  "$schema": "http://json-schema.org/draft-07/schema#",
  "title": "ClaimVerification",
  "type": "object",
  "required": ["verdict", "confidence", "explanation"],
  "additionalProperties": false,
  "properties": {
    "verdict":     { "type": "string", "enum": ["entailed", "not_entailed", "unclear"], "description": "Whether the cited memories support the claim." },
    "confidence":  { "type": "number", "minimum": 0, "maximum": 1, "description": "Confidence in the verdict." },
    "explanation": { "type": "string", "description": "A short rationale grounded in the cited memories." }
  }
}`)

const verifySystemPrompt = `You are a claim-verification safeguard for Stowage (verify schema v` + VerifySchemaVersion + `).

You are given a CLAIM and the CITED MEMORIES the author attached to it. Decide whether the cited memories actually ENTAIL the claim:
- "entailed": the cited memories clearly support the claim.
- "not_entailed": the cited memories do not support (or contradict) the claim.
- "unclear": the memories are insufficient to decide.

Judge ONLY against the cited memories — do not use outside knowledge. Be strict: a plausible-but-unsupported claim is "not_entailed".

Return a valid JSON object matching the response schema — no prose, no markdown fences.`

type verifyOut struct {
	Verdict     string  `json:"verdict"`
	Confidence  float64 `json:"confidence"`
	Explanation string  `json:"explanation"`
}

// BuildVerifyPrompt assembles the (system, user) verification prompt. Pure +
// deterministic (golden-tested).
func BuildVerifyPrompt(claim string, cited []CitedMemory) (system, user string) {
	b := &writer{}
	b.line("Claim:")
	b.line(claim)
	b.line("")
	b.line("Cited memories:")
	for i, c := range cited {
		b.linef("[%d] %s", i+1, c.Content)
	}
	return verifySystemPrompt, b.String()
}

// Verify runs the entailment check (schema-constrained Complete, P5/D-040). A gateway
// error ⇒ {Verdict:unclear, Degraded:true}, NO error (D-036). Empty cited ⇒ unclear,
// no gateway call.
func Verify(ctx context.Context, gw gateway.Gateway, claim string, cited []CitedMemory) (Verdict, error) {
	if gw == nil || len(cited) == 0 {
		return Verdict{Verdict: VerdictUnclear, Degraded: gw == nil}, nil
	}
	system, user := BuildVerifyPrompt(claim, cited)
	resp, err := gw.Complete(ctx, gateway.CompleteRequest{
		System:      system,
		Messages:    []gateway.Message{{Role: "user", Content: user}},
		Schema:      verifySchema,
		MaxTokens:   verifyMaxTokens,
		Temperature: 0.0,
	})
	if err != nil {
		return Verdict{Verdict: VerdictUnclear, Degraded: true}, nil
	}
	var vo verifyOut
	if err := json.Unmarshal(resp.JSON, &vo); err != nil {
		return Verdict{}, fmt.Errorf("trust: verify decode: %w", err)
	}
	if vo.Verdict != VerdictEntailed && vo.Verdict != VerdictNotEntailed && vo.Verdict != VerdictUnclear {
		vo.Verdict = VerdictUnclear
	}
	return Verdict{Verdict: vo.Verdict, Confidence: vo.Confidence, Explanation: vo.Explanation}, nil
}

// ResolveCited resolves citation handles (injection IDs, §5.7) to the cited memories,
// scope-enforced (P3). Handles that don't resolve are skipped (best-effort; the verdict
// reflects what could be resolved). Shared by all three surfaces (D-067).
func ResolveCited(ctx context.Context, st store.Store, scope identity.Scope, citations []string) ([]CitedMemory, error) {
	memIDs := make([]string, 0, len(citations))
	seen := make(map[string]bool)
	for _, c := range citations {
		inj, err := st.Injections().Get(ctx, scope, c)
		if err != nil {
			continue // unknown handle — skip
		}
		if !seen[inj.MemoryID] {
			seen[inj.MemoryID] = true
			memIDs = append(memIDs, inj.MemoryID)
		}
	}
	if len(memIDs) == 0 {
		return nil, nil
	}
	mems, err := st.Memories().GetMany(ctx, scope, memIDs)
	if err != nil {
		return nil, fmt.Errorf("trust: resolve cited: %w", err)
	}
	out := make([]CitedMemory, 0, len(mems))
	for _, m := range mems {
		out = append(out, CitedMemory{ID: m.ID, Content: m.Content})
	}
	return out, nil
}

// writer is a tiny strings.Builder wrapper for prompt assembly.
type writer struct{ b []byte }

func (w *writer) line(s string)            { w.b = append(w.b, s...); w.b = append(w.b, '\n') }
func (w *writer) linef(f string, a ...any) { w.line(fmt.Sprintf(f, a...)) }
func (w *writer) String() string           { return string(w.b) }
