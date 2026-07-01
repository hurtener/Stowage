// Package reflect implements the Phase 19 reflection write-side (ACE §6a.2,
// D-077): it distills `strategy` and `failure_mode` candidate memories from
// outcome-tagged task trajectories via the gateway seam. The candidates are
// reconciled like any other (dedupe/trust/supersede) — this package only builds
// them. The deterministic playbook *assembly* (the read side) is LLM-free and
// lives in internal/playbook (D-072); this package is the LLM-ful counterpart.
//
// Reflection runs as a lifecycle sweep (internal/lifecycle), never on the ingest
// hot path (P2). All model access is through gateway.Gateway (P5), schema-
// constrained (§10).
package reflect

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/hurtener/stowage/internal/gateway"
	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/pipeline"
	"github.com/hurtener/stowage/internal/store"
)

const (
	// reflectMaxTokens gives the model reasoning headroom; a tight cap truncates
	// the JSON on thinking models (the 2026-06-12 lesson). Mirrors extract/judge.
	reflectMaxTokens = 8192
	// reflectSeedStability seeds reflection memories' decay stability (D-077 #4).
	reflectSeedStability = 1.0
	// ReflectionTrustSource is the provenance trust source stamped on reflection
	// memories, distinguishing them from topic-extracted ones (D-077 #4).
	ReflectionTrustSource = "llm_reflected"
)

// Trajectory is the unit a reflection pass reflects over: outcome-tagged records
// of a single (project, user, session, branch) identity, ordered by occurred_at,
// plus the terminal outcome (D-077 #2). Grouping on the full identity — not just
// session — prevents two users' records merging if session IDs collide within a
// tenant.
type Trajectory struct {
	ProjectID string
	UserID    string
	SessionID string
	BranchID  string
	Records   []store.Record
	Outcome   string // "success" | "failure"
}

// Key is a stable identifier for the trajectory (sorted record IDs + outcome),
// used by the sweep as a job marker so a trajectory is reflected once per epoch
// (D-077 #6). Stable across runs regardless of record ordering.
func (t Trajectory) Key() string {
	ids := make([]string, len(t.Records))
	for i, r := range t.Records {
		ids[i] = r.ID
	}
	sort.Strings(ids)
	h := sha256.New()
	for _, id := range ids {
		_, _ = h.Write([]byte(id))
		_, _ = h.Write([]byte{0})
	}
	_, _ = h.Write([]byte(t.Outcome))
	return hex.EncodeToString(h.Sum(nil))[:32]
}

// AssembleTrajectories groups outcome-tagged records into trajectories keyed on
// the full identity (project, user, session, branch). It is order-robust: records
// are bucketed by a map, so it does not depend on the input being pre-sorted, and
// first-seen identity order is preserved for determinism. Within a trajectory,
// records keep input order (ListByOutcome returns occurred_at-ascending). The
// terminal outcome is the last record's outcome.
func AssembleTrajectories(records []store.Record) []Trajectory {
	type key struct{ project, user, session, branch string }
	idx := make(map[key]int)
	var out []Trajectory
	for _, r := range records {
		k := key{r.ProjectID, r.UserID, r.SessionID, r.BranchID}
		i, ok := idx[k]
		if !ok {
			i = len(out)
			idx[k] = i
			out = append(out, Trajectory{ProjectID: r.ProjectID, UserID: r.UserID, SessionID: r.SessionID, BranchID: r.BranchID})
		}
		out[i].Records = append(out[i].Records, r)
		if r.Outcome != "" {
			out[i].Outcome = r.Outcome // terminal outcome = last tagged record
		}
	}
	return out
}

// reflectionItem is the per-reflection shape the model returns (mirrors the
// schema). Decoded from the VALIDATED gateway resp.JSON.
type reflectionItem struct {
	Kind               string              `json:"kind"`
	Content            string              `json:"content"`
	Context            string              `json:"context"`
	Entities           []string            `json:"entities"`
	Keywords           []string            `json:"keywords"`
	AnticipatedQueries []string            `json:"anticipated_queries"`
	Importance         int                 `json:"importance"`
	Confidence         float64             `json:"confidence"`
	Provenance         []pipeline.ProvSpan `json:"provenance"`
}

type reflectionResponse struct {
	Reflections []reflectionItem `json:"reflections"`
}

// Reflect runs the reflection gateway call over one trajectory and returns the
// produced candidates (kind ∈ {strategy, failure_mode}), stamped with the
// reflection trust source + seed stability. Returns an empty slice (not an error)
// when the model finds no transferable lesson. The caller emits these as a
// pipeline.CandidateBatch into the reconcile stage.
// model optionally overrides the gateway's configured completion model ("" =
// gateway.model); set from gateway.reflect_model (D-132).
func Reflect(ctx context.Context, gw gateway.Gateway, scope identity.Scope, t Trajectory, model string) ([]pipeline.Candidate, error) {
	if len(t.Records) == 0 {
		return nil, nil
	}
	system, user := BuildReflectionPrompt(t)
	resp, err := gw.Complete(ctx, gateway.CompleteRequest{
		System:      system,
		Messages:    []gateway.Message{{Role: "user", Content: user}},
		Schema:      reflectionSchema,
		MaxTokens:   reflectMaxTokens,
		Temperature: 0.0,
		Model:       model, // "" → gateway.model (D-132)
	})
	if err != nil {
		return nil, fmt.Errorf("reflect: gateway complete: %w", err)
	}
	var rr reflectionResponse
	if err := json.Unmarshal(resp.JSON, &rr); err != nil {
		return nil, fmt.Errorf("reflect: decode reflections: %w", err)
	}

	// Valid record IDs for provenance clamping (P1: provenance must reference real
	// records in the trajectory).
	validIDs := make(map[string]bool, len(t.Records))
	for _, r := range t.Records {
		validIDs[r.ID] = true
	}

	out := make([]pipeline.Candidate, 0, len(rr.Reflections))
	for _, it := range rr.Reflections {
		if !pipeline.IsReflectionKind(it.Kind) || it.Content == "" {
			continue // schema should prevent this; defend anyway
		}
		prov := make([]pipeline.ProvSpan, 0, len(it.Provenance))
		for _, p := range it.Provenance {
			if validIDs[p.RecordID] {
				prov = append(prov, p)
			}
		}
		if len(prov) == 0 {
			continue // a memory without valid provenance fails validation (P1)
		}
		out = append(out, pipeline.Candidate{
			Kind:               it.Kind,
			Content:            it.Content,
			Context:            it.Context,
			Entities:           it.Entities,
			Keywords:           it.Keywords,
			AnticipatedQueries: it.AnticipatedQueries,
			Importance:         it.Importance,
			Confidence:         it.Confidence,
			Provenance:         prov,
			TrustSource:        ReflectionTrustSource,
			Stability:          reflectSeedStability,
		})
	}
	return out, nil
}
