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
	"time"

	"github.com/oklog/ulid/v2"

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

// VerifyClaim is the full claim-verification core the surfaces call (D-067): it
// resolves the citation handles, runs the entailment check, and — for the reasoning
// trace (Phase 26, D-086) — captures the verdict as a verify.verdict event keyed by
// the response_id the citations belong to. Degraded-safe (gateway failure ⇒
// unclear+degraded, no error). The capture is best-effort and never fails the verify.
func VerifyClaim(ctx context.Context, st store.Store, gw gateway.Gateway, scope identity.Scope, claim string, citations []string) (Verdict, error) {
	cited, responseIDs, err := resolveCitedWithResponse(ctx, st, scope, citations)
	if err != nil {
		return Verdict{}, err
	}
	v, err := Verify(ctx, gw, claim, cited)
	if err != nil {
		return Verdict{}, err
	}
	// Capture the verdict against EVERY distinct response the citations belong to
	// (A8, D-094) — a caller may cite memories injected across several responses, and
	// each response's reasoning trace must record the verdict for the claim it supported.
	for _, responseID := range responseIDs {
		emitVerdictEvent(ctx, st, scope, responseID, claim, v)
	}
	return v, nil
}

// resolveCitedWithResponse resolves citations to memories AND returns every DISTINCT
// response_id the citations belong to (in first-seen order), for trace capture. Scope-
// enforced (P3). A verify call normally cites one response, but a caller may mix
// citations from several; each distinct response records the verdict (A8, D-094).
func resolveCitedWithResponse(ctx context.Context, st store.Store, scope identity.Scope, citations []string) ([]CitedMemory, []string, error) {
	memIDs := make([]string, 0, len(citations))
	seen := make(map[string]bool)
	responseIDs := make([]string, 0, 1)
	seenResp := make(map[string]bool)
	for _, c := range citations {
		inj, err := st.Injections().Get(ctx, scope, c)
		if err != nil {
			continue
		}
		if inj.ResponseID != "" && !seenResp[inj.ResponseID] {
			seenResp[inj.ResponseID] = true
			responseIDs = append(responseIDs, inj.ResponseID)
		}
		if !seen[inj.MemoryID] {
			seen[inj.MemoryID] = true
			memIDs = append(memIDs, inj.MemoryID)
		}
	}
	if len(memIDs) == 0 {
		return nil, responseIDs, nil
	}
	mems, err := st.Memories().GetMany(ctx, scope, memIDs)
	if err != nil {
		return nil, nil, fmt.Errorf("trust: resolve cited: %w", err)
	}
	out := make([]CitedMemory, 0, len(mems))
	for _, m := range mems {
		out = append(out, CitedMemory{ID: m.ID, Content: m.Content})
	}
	return out, responseIDs, nil
}

// emitVerdictEvent persists the verdict for the reasoning trace (Phase 26, D-086):
// a verify.verdict event keyed by response_id. Best-effort.
func emitVerdictEvent(ctx context.Context, st store.Store, scope identity.Scope, responseID, claim string, v Verdict) {
	payload, err := json.Marshal(struct {
		Claim      string  `json:"claim"`
		Verdict    string  `json:"verdict"`
		Confidence float64 `json:"confidence"`
		Degraded   bool    `json:"degraded"`
	}{Claim: claim, Verdict: v.Verdict, Confidence: v.Confidence, Degraded: v.Degraded})
	if err != nil {
		return
	}
	_ = st.Events().Emit(ctx, scope, store.Event{
		ID: ulid.Make().String(), TenantID: scope.Tenant, ProjectID: scope.Project, UserID: scope.User,
		Type: "verify.verdict", SubjectID: responseID, Reason: "verify: verdict captured for the reasoning trace",
		Payload: string(payload), CreatedAt: time.Now().UnixMilli(),
	})
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
