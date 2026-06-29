package harness

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/hurtener/stowage/eval/gain"
	"github.com/hurtener/stowage/internal/gateway"
	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/playbook"
	"github.com/hurtener/stowage/internal/reconcile"
	"github.com/hurtener/stowage/internal/reflect"
	"github.com/hurtener/stowage/internal/store"
)

// online-adaptation harness (Phase 20b, D-078): runs an AdaptScenario's tasks in
// order; between tasks the Phase-19 reflection→playbook loop accumulates strategies,
// and each task's eval question is answered by the reader with the assembled
// playbook as context. The quality trajectory across tasks is the compounding
// signal (ACE). Reported, not release-gated.
//
// Harness simplification (documented, D-078): reflection candidates are committed
// to the store directly here, not fed through the reconcile stage — the production
// reconcile-fed path is proven by the Phase-19 integration test; this harness
// measures the loop's EFFECT (does the growing playbook help the next task?).

// AdaptTaskResult is one task's outcome in an adaptation run.
type AdaptTaskResult struct {
	TaskIndex     int     `json:"task_index"`
	Outcome       string  `json:"outcome"`
	Question      string  `json:"question"`
	Expected      string  `json:"expected"`
	Answer        string  `json:"answer"`
	Verdict       string  `json:"verdict"`
	Quality       float64 `json:"quality"`
	PlaybookItems int     `json:"playbook_items"`
}

// AdaptResult is the trajectory across an AdaptScenario's tasks.
type AdaptResult struct {
	ScenarioID   string            `json:"scenario_id"`
	Tasks        []AdaptTaskResult `json:"tasks"`
	FirstQuality float64           `json:"first_quality"`
	LastQuality  float64           `json:"last_quality"`
	Delta        float64           `json:"delta"` // last − first: positive = compounding improvement
}

// playbookContext flattens a playbook's item contents into reader-context lines.
func playbookContext(pb *playbook.Playbook) []string {
	var out []string
	if pb == nil {
		return out
	}
	for _, sec := range pb.Sections {
		for _, it := range sec.Items {
			out = append(out, it.Content)
		}
	}
	return out
}

// reflectAndCommit reflects one trajectory and commits its candidates to the store.
func reflectAndCommit(ctx context.Context, st store.Store, gw gateway.Gateway, scope identity.Scope, traj reflect.Trajectory) (int, error) {
	cands, err := reflect.Reflect(ctx, gw, scope, traj, "") // "" → the harness-configured gateway.model (D-132)
	if err != nil {
		return 0, err
	}
	now := time.Now().UnixMilli()
	committed := 0
	for _, c := range cands {
		memID := ulid.Make().String()
		// Use the SAME normalization + hash as production reconcile so dedup
		// decisions match (the comment below is then accurate).
		hash := reconcile.ContentHash(reconcile.NormalizeContent(c.Content))
		mem := store.Memory{
			ID:          memID,
			Kind:        c.Kind,
			Content:     c.Content,
			Context:     c.Context,
			Status:      "active",
			Importance:  c.Importance,
			Confidence:  c.Confidence,
			TrustSource: c.TrustSource,
			Stability:   c.Stability,
			ContentHash: hash,
			CreatedAt:   now,
			UpdatedAt:   now,
		}
		// Carry provenance (P1) + anticipated queries through the commit, as the
		// production reconcile path does.
		prov := make([]store.Provenance, 0, len(c.Provenance))
		for _, p := range c.Provenance {
			prov = append(prov, store.Provenance{MemoryID: memID, RecordID: p.RecordID, SpanStart: p.SpanStart, SpanEnd: p.SpanEnd, TenantID: scope.Tenant})
		}
		err := st.Memories().Commit(ctx, scope, store.CommitSet{
			Action:     store.ActionAdd,
			Memory:     mem,
			Entities:   c.Entities,
			Keywords:   c.Keywords,
			Queries:    c.AnticipatedQueries,
			Provenance: prov,
			Scope:      scope,
		})
		if errors.Is(err, store.ErrDuplicateContent) {
			// The same lesson already in the playbook — this is the dedup outcome
			// reconcile would reach; skip (harness-direct commit, D-078).
			continue
		}
		if err != nil {
			return committed, fmt.Errorf("commit reflection memory: %w", err)
		}
		committed++
	}
	return committed, nil
}

// RunAdaptScenario runs the tasks in order, reflecting between them, and measures
// the quality trajectory with the playbook injected as reader context. Needs a live
// gateway (operator-run via adaptmode_test.go).
func RunAdaptScenario(ctx context.Context, srv *TestServer, gw gateway.Gateway, sc *gain.AdaptScenario, budget int) (AdaptResult, error) {
	tenant := identity.Scope{Tenant: srv.TenantID}
	res := AdaptResult{ScenarioID: sc.ID}
	reflected := map[string]bool{}

	for i, task := range sc.Tasks {
		// Ingest the task's turns as outcome-tagged records under a per-task session
		// scope (session_id is scope-level), so each task is its own trajectory.
		taskScope := identity.Scope{Tenant: srv.TenantID, Project: "adapt", User: "u", Session: fmt.Sprintf("%s-t%d", sc.ID, i)}
		now := time.Now().UnixMilli()
		recs := make([]store.Record, 0, len(task.Turns))
		for j, turn := range task.Turns {
			recs = append(recs, store.Record{
				ID:         ulid.Make().String(),
				BranchID:   "main",
				Role:       turn.Role,
				Content:    turn.Content,
				Outcome:    task.Outcome, // tag the task's records with its outcome
				OccurredAt: now + int64(j),
				CreatedAt:  now + int64(j),
			})
		}
		if err := srv.Store.Records().Append(ctx, taskScope, recs); err != nil {
			return res, fmt.Errorf("append task %d: %w", i, err)
		}

		// Reflect over all outcome-tagged trajectories not yet reflected this run.
		tagged, err := srv.Store.Records().ListByOutcome(ctx, tenant, []string{"success", "failure"}, 0, 1000)
		if err != nil {
			return res, fmt.Errorf("list outcomes task %d: %w", i, err)
		}
		for _, traj := range reflect.AssembleTrajectories(tagged) {
			if traj.Outcome == "" || reflected[traj.Key()] {
				continue
			}
			reflected[traj.Key()] = true
			if _, err := reflectAndCommit(ctx, srv.Store, gw, tenant, traj); err != nil {
				return res, fmt.Errorf("reflect task %d: %w", i, err)
			}
		}

		// Assemble the playbook and answer the eval question with it as context.
		pb, err := playbook.Assemble(ctx, srv.Store, tenant, playbook.Options{TokenBudget: budget})
		if err != nil {
			return res, fmt.Errorf("playbook task %d: %w", i, err)
		}
		pbCtx := playbookContext(pb)
		jr, err := JudgeQuestion(ctx, gw, task.EvalQuestion, task.ExpectedAnswer, pbCtx)
		if err != nil {
			return res, fmt.Errorf("judge task %d: %w", i, err)
		}
		q := quality(jr.Verdict)
		res.Tasks = append(res.Tasks, AdaptTaskResult{
			TaskIndex: i, Outcome: task.Outcome, Question: task.EvalQuestion, Expected: task.ExpectedAnswer,
			Answer: jr.Answer, Verdict: jr.Verdict, Quality: q, PlaybookItems: len(pbCtx),
		})
	}

	if len(res.Tasks) > 0 {
		res.FirstQuality = res.Tasks[0].Quality
		res.LastQuality = res.Tasks[len(res.Tasks)-1].Quality
		res.Delta = res.LastQuality - res.FirstQuality
	}
	return res, nil
}
