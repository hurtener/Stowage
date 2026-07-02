package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/hurtener/dockyard/runtime/tool"

	"github.com/hurtener/stowage/internal/causal"
	"github.com/hurtener/stowage/internal/config"
	"github.com/hurtener/stowage/internal/episodes"
	"github.com/hurtener/stowage/internal/grants"
	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/pipeline"
	"github.com/hurtener/stowage/internal/playbook"
	"github.com/hurtener/stowage/internal/proactive"
	"github.com/hurtener/stowage/internal/reconcile"
	"github.com/hurtener/stowage/internal/records"
	"github.com/hurtener/stowage/internal/retrieval"
	"github.com/hurtener/stowage/internal/store"
	"github.com/hurtener/stowage/internal/topics"
	"github.com/hurtener/stowage/internal/traces"
	"github.com/hurtener/stowage/internal/trust"
)

// ─── memory_ingest ────────────────────────────────────────────────────────────

func makeIngestHandler(svc *Services) tool.Handler[IngestInput, IngestOutput] {
	return func(ctx context.Context, in IngestInput) (tool.Result[IngestOutput], error) {
		scope, err := svc.ScopeFn(ctx)
		if err != nil {
			return tool.Result[IngestOutput]{}, fmt.Errorf("memory_ingest: resolve scope: %w", err)
		}
		// D-138 tenant guard (write path; identity discarded — ingest's scope is a
		// per-record/explicit-target write, deliberately not _meta-narrowed, ae2).
		if _, err := readMetaIdentity(ctx, scope.Tenant); err != nil {
			return tool.Result[IngestOutput]{}, fmt.Errorf("memory_ingest: %w", err)
		}

		if len(in.Records) == 0 {
			return tool.Result[IngestOutput]{}, fmt.Errorf("memory_ingest: records must not be empty")
		}

		// Contribute-mode honoring (D-071): when target_scope is set the records
		// are committed into the pool-owner's scope, subject to an active contribute
		// grant covering the caller. The grant-check + scope-override is the shared
		// grants.AuthorizeContribute core the HTTP /v1/records path also uses, so
		// the two server surfaces cannot drift. Without a covering grant the request
		// is rejected (never silently mis-scoped). h2's fail-loud is replaced.
		contributeMode := in.TargetScope != nil
		var contribute grants.ContributeContext
		if contributeMode || in.ContributorUserID != "" {
			if svc.GrantsSvc == nil {
				return tool.Result[IngestOutput]{}, fmt.Errorf("memory_ingest: grants service not available")
			}
			if in.TargetScope == nil {
				return tool.Result[IngestOutput]{}, fmt.Errorf("memory_ingest: contributor_user_id requires target_scope")
			}
			callerScope := identity.Scope{Tenant: scope.Tenant}
			targetScope := identity.Scope{
				Tenant:  scope.Tenant, // contribute is always within the caller's tenant
				Project: in.TargetScope.ProjectID,
				User:    in.TargetScope.UserID,
				Session: in.TargetScope.SessionID,
			}
			contribute, err = svc.GrantsSvc.AuthorizeContribute(ctx, callerScope, targetScope, in.ContributorUserID)
			if err != nil {
				if errors.Is(err, grants.ErrNotCovered) || errors.Is(err, grants.ErrCrossTenantGrant) {
					return tool.Result[IngestOutput]{}, fmt.Errorf("memory_ingest: no active contribute grant for target scope")
				}
				return tool.Result[IngestOutput]{}, fmt.Errorf("memory_ingest: contribute check: %w", err)
			}
		}

		// Stamp and validate each record.
		type stampedItem struct {
			rec       records.Record
			bufferKey string
		}
		stamped := make([]stampedItem, 0, len(in.Records))
		for i, item := range in.Records {
			if item.Role == "" {
				item.Role = "user" // sensible default for MCP
			}
			// In contribute mode, scope fields are overridden with the target
			// pool-owner scope via the shared core (D-071).
			recProjectID := item.ProjectID
			recUserID := item.UserID
			recSessionID := item.SessionID
			if contributeMode {
				recProjectID, recUserID, recSessionID = contribute.ApplyTo(recProjectID, recUserID, recSessionID)
			}
			rec, err := records.New(records.Input{
				TenantID:      scope.Tenant,
				ProjectID:     recProjectID,
				UserID:        recUserID,
				SessionID:     recSessionID,
				BranchID:      item.BranchID,
				Role:          item.Role,
				Content:       item.Content,
				SourceAgent:   item.SourceAgent,
				ResponseID:    item.ResponseID,
				Outcome:       item.Outcome,
				OutcomeDetail: item.OutcomeDetail,
				OccurredAt:    item.OccurredAt,
			})
			if err != nil {
				return tool.Result[IngestOutput]{}, fmt.Errorf("memory_ingest: item[%d]: %w", i, err)
			}
			stamped = append(stamped, stampedItem{rec: *rec, bufferKey: item.BufferKey})
		}

		// Build store records.
		storeRecs := make([]store.Record, len(stamped))
		for i, si := range stamped {
			r := si.rec
			storeRecs[i] = store.Record{
				ID:            r.ID,
				TenantID:      r.TenantID,
				ProjectID:     r.ProjectID,
				UserID:        r.UserID,
				SessionID:     r.SessionID,
				BranchID:      r.BranchID,
				Role:          r.Role,
				Content:       r.Content,
				SourceAgent:   r.SourceAgent,
				ResponseID:    r.ResponseID,
				Outcome:       r.Outcome,
				OutcomeDetail: r.OutcomeDetail,
				TokenEstimate: r.TokenEstimate,
				OccurredAt:    r.OccurredAt,
				CreatedAt:     r.CreatedAt,
			}
		}

		if err := svc.Store.Records().Append(ctx, scope, storeRecs); err != nil {
			return tool.Result[IngestOutput]{}, fmt.Errorf("memory_ingest: store: %w", err)
		}

		// Non-blocking, panic-safe pipeline enqueue (P2). Uses the shared
		// pipeline.TrySend so a send racing the shutdown Drain (channel closed)
		// degrades to Enqueued=false instead of panicking across the MCP boundary
		// — the same helper the SDK uses (D-067 lens, parity defense-in-depth).
		allEnqueued := true
		for _, si := range stamped {
			if !pipeline.TrySend(svc.PipelineIn, pipeline.Item{
				RecordID:  si.rec.ID,
				TenantID:  scope.Tenant,
				BufferKey: si.bufferKey,
				SessionID: si.rec.SessionID,
				BranchID:  si.rec.BranchID,
			}) {
				allEnqueued = false
			}
		}

		ids := make([]string, len(stamped))
		for i, si := range stamped {
			ids[i] = si.rec.ID
		}

		out := IngestOutput{IDs: ids, Enqueued: allEnqueued}
		return tool.Result[IngestOutput]{
			Text:       fmt.Sprintf("Ingested %d record(s); enqueued=%v", len(ids), allEnqueued),
			Structured: out,
		}, nil
	}
}

// ─── memory_retrieve ──────────────────────────────────────────────────────────

func makeRetrieveHandler(svc *Services) tool.Handler[RetrieveInput, RetrieveOutput] {
	return func(ctx context.Context, in RetrieveInput) (tool.Result[RetrieveOutput], error) {
		// Tenant is the auth boundary; project/user/agent are resolved through the
		// ONE cross-surface resolver (ae8, D-148/D-067/D-073), which applies the
		// D-137 precedence (verified JWT claims > _meta > args) and the D-138
		// tenant guard. Empty = tenant-wide (back-compat).
		scope, effSession, err := resolveScope(svc, ctx, scopeArgs{Project: in.ProjectID, User: in.UserID, Session: in.SessionID})
		if err != nil {
			return tool.Result[RetrieveOutput]{}, fmt.Errorf("memory_retrieve: %w", err)
		}

		if in.Query == "" {
			return tool.Result[RetrieveOutput]{}, fmt.Errorf("memory_retrieve: query must not be empty")
		}

		if svc.Retriever == nil {
			return tool.Result[RetrieveOutput]{}, fmt.Errorf("memory_retrieve: retriever not available")
		}

		resp, err := svc.Retriever.Retrieve(ctx, scope, retrieval.Request{
			Query:        in.Query,
			Limit:        in.Limit,
			Window:       store.Window{From: in.From, Until: in.Until},
			Kinds:        in.Kinds,
			IncludeLanes: in.IncludeLanes,
			// Session-REPLACE (D-137/D-150): the effective session (claim > _meta >
			// arg) fed to this EXISTING relevance sink — never onto Scope.Session
			// (that would add a new store predicate; D-150).
			SessionID:     effSession,
			Debug:         in.Debug,
			ResponseID:    in.ResponseID,
			Profile:       in.Profile,
			IncludeTopics: in.IncludeTopics,
			ExcludeTopics: in.ExcludeTopics,
		})
		if err != nil {
			return tool.Result[RetrieveOutput]{}, fmt.Errorf("memory_retrieve: %w", err)
		}

		// Build the shared render-input projection (D-141); its Citation/EpisodeID
		// slots feed both the Structured item mapping below AND the rendered Text
		// body (via retrieval.RenderReadBody below, D-142).
		renderItems := retrieval.RenderItemsFromMemoryItems(resp.Items)

		items := make([]RetrieveItem, len(resp.Items))
		for i, it := range resp.Items {
			ri := RetrieveItem{
				ID:       it.Memory.ID,
				Kind:     it.Memory.Kind,
				Content:  renderItems[i].Content,
				Context:  it.Memory.Context,
				Score:    it.Score,
				Citation: renderItems[i].Citation,
			}
			if it.Stale {
				ri.Stale = true
				ri.SupersededBy = it.Memory.SupersededByID
				ri.SupersededByContent = renderItems[i].SupersededByContent
				ri.SupersededByDate = renderItems[i].SupersededByDate
			}
			ri.OccurredAt = renderItems[i].OccurredAt
			if in.IncludeLanes {
				ri.Lanes = it.Lanes
			}
			items[i] = ri
		}

		conflicts := make([]ConflictPair, len(resp.Support.Conflicts))
		for i, c := range resp.Support.Conflicts {
			conflicts[i] = ConflictPair{A: c.A, B: c.B}
		}

		out := RetrieveOutput{
			ResponseID: resp.ResponseID,
			Items:      items,
			Support: RetrieveSupport{
				Strength:  resp.Support.Strength,
				TopScore:  resp.Support.TopScore,
				Conflicts: conflicts,
			},
			Degraded:            resp.Degraded,
			DegradedRerank:      resp.DegradedRerank,
			DegradedTopicFilter: resp.DegradedTopicFilter,
			DegradedAgentFilter: resp.DegradedAgentFilter,
			CacheHit:            resp.CacheHit,
			API:                 resp.API,
		}
		// Lean MCP read (D-142, ae4a): the model-facing Text is the rendered
		// markdown body — episode hooks + per-item [cite:…] drill handles — not
		// a count string. Structured keeps the full typed result unchanged.
		return tool.Result[RetrieveOutput]{
			Text:       retrieval.RenderReadBody(resp.Items),
			Structured: out,
		}, nil
	}
}

// ─── memory_playbook ─────────────────────────────────────────────────────────

func makePlaybookHandler(svc *Services) tool.Handler[PlaybookInput, PlaybookOutput] {
	return func(ctx context.Context, in PlaybookInput) (tool.Result[PlaybookOutput], error) {
		scope, effSession, err := resolveScope(svc, ctx, scopeArgs{Project: in.ProjectID, User: in.UserID, Session: in.SessionID})
		if err != nil {
			return tool.Result[PlaybookOutput]{}, fmt.Errorf("memory_playbook: %w", err)
		}

		// LLM-free assembly core (D-072) — identical to GET /v1/playbook + the
		// embedded SDK Playbook. The token budget is profile-internal (D-042).
		// Session-REPLACE (D-137/D-150): existing sink, never Scope.Session.
		pb, err := playbook.Assemble(ctx, svc.Store, scope, playbook.Options{
			SessionID:   effSession,
			TokenBudget: config.PlaybookBudgetForProfile(svc.Profile),
		})
		if err != nil {
			return tool.Result[PlaybookOutput]{}, fmt.Errorf("memory_playbook: %w", err)
		}

		out := playbookToOutput(pb)
		return tool.Result[PlaybookOutput]{
			Text:       fmt.Sprintf("Playbook: %d section(s), %d item(s) packed (%d/%d tokens)", len(out.Sections), pb.Budget.ItemsPacked, pb.Budget.TokensUsed, pb.Budget.TokenBudget),
			Structured: out,
		}, nil
	}
}

// playbookToOutput maps the assembled playbook onto the MCP wire type. Shared
// shape with the HTTP + SDK surfaces so all three are byte-identical (AC-5).
func playbookToOutput(pb *playbook.Playbook) PlaybookOutput {
	out := PlaybookOutput{
		Sections: make([]PlaybookSection, 0, len(pb.Sections)),
		Budget: PlaybookBudget{
			TokenBudget: pb.Budget.TokenBudget,
			TokensUsed:  pb.Budget.TokensUsed,
			ItemsTotal:  pb.Budget.ItemsTotal,
			ItemsPacked: pb.Budget.ItemsPacked,
		},
	}
	for _, sec := range pb.Sections {
		ms := PlaybookSection{Title: sec.Title, Kind: sec.Kind, Items: make([]PlaybookItem, 0, len(sec.Items))}
		for _, it := range sec.Items {
			mi := PlaybookItem{MemoryID: it.MemoryID, Kind: it.Kind, Content: it.Content, Score: it.Score}
			for _, p := range it.Provenance {
				mi.Provenance = append(mi.Provenance, PlaybookProvRef{
					RecordID: p.RecordID, SpanStart: p.SpanStart, SpanEnd: p.SpanEnd,
				})
			}
			ms.Items = append(ms.Items, mi)
		}
		out.Sections = append(out.Sections, ms)
	}
	return out
}

// ─── memory_drilldown ────────────────────────────────────────────────────────

func makeDrilldownHandler(svc *Services) tool.Handler[DrilldownInput, DrilldownOutput] {
	return func(ctx context.Context, in DrilldownInput) (tool.Result[DrilldownOutput], error) {
		scope, _, err := resolveScope(svc, ctx, scopeArgs{Project: in.ProjectID, User: in.UserID})
		if err != nil {
			return tool.Result[DrilldownOutput]{}, fmt.Errorf("memory_drilldown: %w", err)
		}

		if in.MemoryID == "" && in.Citation == "" {
			return tool.Result[DrilldownOutput]{}, fmt.Errorf("memory_drilldown: one of memory_id or citation must be set")
		}
		if in.MemoryID != "" && in.Citation != "" {
			return tool.Result[DrilldownOutput]{}, fmt.Errorf("memory_drilldown: only one of memory_id or citation may be set")
		}

		memoryID := in.MemoryID

		// Resolve citation → memory_id via injection store.
		if in.Citation != "" {
			inj, err := svc.Store.Injections().Get(ctx, scope, in.Citation)
			if err != nil {
				return tool.Result[DrilldownOutput]{}, fmt.Errorf("memory_drilldown: resolve citation: %w", err)
			}
			memoryID = inj.MemoryID
		}

		junctions, err := svc.Store.Memories().GetJunctions(ctx, scope, memoryID)
		if err != nil {
			return tool.Result[DrilldownOutput]{}, fmt.Errorf("memory_drilldown: get junctions: %w", err)
		}

		if len(junctions.Provenance) == 0 {
			out := DrilldownOutput{MemoryID: memoryID, Spans: []DrilldownSpan{}}
			return tool.Result[DrilldownOutput]{
				Text:       fmt.Sprintf("No provenance spans for memory %s", memoryID),
				Structured: out,
			}, nil
		}

		// Batch-fetch referenced records.
		seen := make(map[string]bool, len(junctions.Provenance))
		recordIDs := make([]string, 0, len(junctions.Provenance))
		for _, p := range junctions.Provenance {
			if !seen[p.RecordID] {
				recordIDs = append(recordIDs, p.RecordID)
				seen[p.RecordID] = true
			}
		}

		recs, err := svc.Store.Records().GetMany(ctx, scope, recordIDs)
		if err != nil {
			return tool.Result[DrilldownOutput]{}, fmt.Errorf("memory_drilldown: get records: %w", err)
		}

		recByID := make(map[string]store.Record, len(recs))
		for _, r := range recs {
			recByID[r.ID] = r
		}

		spans := make([]DrilldownSpan, 0, len(junctions.Provenance))
		for _, p := range junctions.Provenance {
			r, ok := recByID[p.RecordID]
			if !ok {
				continue
			}
			spans = append(spans, DrilldownSpan{
				RecordID:   r.ID,
				SpanStart:  p.SpanStart,
				SpanEnd:    p.SpanEnd,
				Excerpt:    retrieval.ClampExcerpt(r.Content, p.SpanStart, p.SpanEnd),
				OccurredAt: r.OccurredAt,
				Role:       r.Role,
			})
		}

		out := DrilldownOutput{MemoryID: memoryID, Spans: spans}
		return tool.Result[DrilldownOutput]{
			Text:       fmt.Sprintf("Drilldown: %d span(s) for memory %s", len(spans), memoryID),
			Structured: out,
		}, nil
	}
}

// ─── memory_feedback ──────────────────────────────────────────────────────────

func makeFeedbackHandler(svc *Services) tool.Handler[FeedbackInput, FeedbackOutput] {
	return func(ctx context.Context, in FeedbackInput) (tool.Result[FeedbackOutput], error) {
		scope, _, err := resolveScope(svc, ctx, scopeArgs{Project: in.ProjectID, User: in.UserID})
		if err != nil {
			return tool.Result[FeedbackOutput]{}, fmt.Errorf("memory_feedback: %w", err)
		}

		if in.Signal == "" {
			return tool.Result[FeedbackOutput]{}, fmt.Errorf("memory_feedback: signal must be set")
		}

		setCount := 0
		if in.ResponseID != "" {
			setCount++
		}
		if in.MemoryID != "" {
			setCount++
		}
		if in.Citation != "" {
			setCount++
		}
		if setCount == 0 {
			return tool.Result[FeedbackOutput]{}, fmt.Errorf("memory_feedback: one of response_id, memory_id, or citation must be set")
		}
		if setCount > 1 {
			return tool.Result[FeedbackOutput]{}, fmt.Errorf("memory_feedback: only one of response_id, memory_id, or citation may be set")
		}

		validMemorySignals := map[string]bool{"use": true, "save": true, "fail": true, "noise": true}

		if in.Citation != "" && in.Signal != "wrong_citation" {
			return tool.Result[FeedbackOutput]{}, fmt.Errorf("memory_feedback: citation-level feedback only accepts signal wrong_citation")
		}
		if in.Citation == "" && !validMemorySignals[in.Signal] {
			return tool.Result[FeedbackOutput]{}, fmt.Errorf("memory_feedback: signal must be one of use|save|fail|noise")
		}

		var applied int

		switch {
		case in.Citation != "":
			if err := svc.Store.Injections().MarkWrongCitation(ctx, scope, in.Citation); err != nil {
				return tool.Result[FeedbackOutput]{}, fmt.Errorf("memory_feedback: wrong_citation: %w", err)
			}
			applied = 1

		case in.MemoryID != "":
			if err := svc.Store.Memories().ApplyFeedback(ctx, scope, in.MemoryID, in.Signal); err != nil {
				return tool.Result[FeedbackOutput]{}, fmt.Errorf("memory_feedback: apply feedback: %w", err)
			}
			applied = 1

		case in.ResponseID != "":
			injections, err := svc.Store.Injections().ListByResponse(ctx, scope, in.ResponseID)
			if err != nil {
				return tool.Result[FeedbackOutput]{}, fmt.Errorf("memory_feedback: list injections: %w", err)
			}
			seen := make(map[string]bool, len(injections))
			for _, inj := range injections {
				if seen[inj.MemoryID] {
					continue
				}
				seen[inj.MemoryID] = true
				if err := svc.Store.Memories().ApplyFeedback(ctx, scope, inj.MemoryID, in.Signal); err != nil {
					svc.Log.WarnContext(ctx, "mcpserver: feedback response apply",
						"memory_id", inj.MemoryID, "err", err)
					continue
				}
				applied++
			}
		}

		out := FeedbackOutput{Applied: applied, Signal: in.Signal}
		return tool.Result[FeedbackOutput]{
			Text:       fmt.Sprintf("Applied signal %q to %d memory record(s)", in.Signal, applied),
			Structured: out,
		}, nil
	}
}

// scopeInvalidator returns the retrieval-cache invalidator the reconcile core
// uses to bust stale results after a content-changing commit (D-053; D-070
// Wave-B checkpoint). It returns an untyped-nil interface when no retriever is
// wired, so the core's nil check is safe. This is why MCP write verbs no longer
// have to remember to invalidate — the core does it.
func (s *Services) scopeInvalidator() reconcile.ScopeInvalidator {
	if s.Retriever != nil {
		return s.Retriever.Cache()
	}
	return nil
}

// ─── memory_assert ────────────────────────────────────────────────────────────

func makeAssertHandler(svc *Services) tool.Handler[AssertInput, AssertOutput] {
	return func(ctx context.Context, in AssertInput) (tool.Result[AssertOutput], error) {
		scope, err := svc.ScopeFn(ctx)
		if err != nil {
			return tool.Result[AssertOutput]{}, fmt.Errorf("memory_assert: resolve scope: %w", err)
		}
		// D-138 tenant guard (write path; identity discarded — assert has no
		// sub-scope arg to narrow, ae2).
		if _, err := readMetaIdentity(ctx, scope.Tenant); err != nil {
			return tool.Result[AssertOutput]{}, fmt.Errorf("memory_assert: %w", err)
		}

		// Shared assert core (D-071) — identical logic to the embedded SDK Assert.
		res, err := reconcile.Assert(ctx, svc.Store, scope, reconcile.AssertParams{
			Action:   in.Action,
			MemoryID: in.MemoryID,
			Content:  in.Content,
			Kind:     in.Kind,
			Context:  in.Context,
			Review:   in.Review,
		}, svc.scopeInvalidator())
		if err != nil {
			return tool.Result[AssertOutput]{}, fmt.Errorf("memory_assert: %w", err)
		}

		out := AssertOutput{MemoryID: res.MemoryID, Action: res.Action, Status: res.Status}
		return tool.Result[AssertOutput]{
			Text:       fmt.Sprintf("Assert %s: memory_id=%s status=%s", res.Action, res.MemoryID, res.Status),
			Structured: out,
		}, nil
	}
}

// ─── memory_get / memory_rollback / memory_resolve (D-070) ────────────────────

// memoryToRecord maps a store.Memory to the MCP wire record.
func memoryToRecord(m store.Memory) MemoryRecord {
	return MemoryRecord{
		ID:             m.ID,
		Kind:           m.Kind,
		Content:        m.Content,
		Context:        m.Context,
		Status:         m.Status,
		Importance:     m.Importance,
		Confidence:     m.Confidence,
		TrustSource:    m.TrustSource,
		MatchCount:     m.MatchCount,
		InjectCount:    m.InjectCount,
		UseCount:       m.UseCount,
		SaveCount:      m.SaveCount,
		FailCount:      m.FailCount,
		NoiseCount:     m.NoiseCount,
		Stability:      m.Stability,
		ValidFrom:      m.ValidFrom,
		ValidUntil:     m.ValidUntil,
		EpisodeID:      m.EpisodeID,
		SupersedesID:   m.SupersedesID,
		SupersededByID: m.SupersededByID,
		PrivacyZone:    m.PrivacyZone,
		ContentHash:    m.ContentHash,
		CreatedAt:      m.CreatedAt,
		UpdatedAt:      m.UpdatedAt,
	}
}

func makeGetHandler(svc *Services) tool.Handler[GetInput, GetOutput] {
	return func(ctx context.Context, in GetInput) (tool.Result[GetOutput], error) {
		scope, _, err := resolveScope(svc, ctx, scopeArgs{Project: in.ProjectID, User: in.UserID})
		if err != nil {
			return tool.Result[GetOutput]{}, fmt.Errorf("memory_get: %w", err)
		}
		if in.MemoryID == "" {
			return tool.Result[GetOutput]{}, fmt.Errorf("memory_get: memory_id must not be empty")
		}
		view, err := reconcile.GetMemory(ctx, svc.Store, scope, in.MemoryID)
		if err != nil {
			return tool.Result[GetOutput]{}, fmt.Errorf("memory_get: %w", err)
		}
		out := GetOutput{
			Memory:          memoryToRecord(view.Memory),
			Entities:        view.Entities,
			Keywords:        view.Keywords,
			Queries:         view.Queries,
			SupersedesChain: view.SupersedesChain,
		}
		for _, p := range view.Provenance {
			out.Provenance = append(out.Provenance, MemoryProvRef{
				RecordID: p.RecordID, SpanStart: p.SpanStart, SpanEnd: p.SpanEnd,
			})
		}
		return tool.Result[GetOutput]{
			Text:       fmt.Sprintf("Memory %s (%s)", out.Memory.ID, out.Memory.Status),
			Structured: out,
		}, nil
	}
}

func makeRollbackHandler(svc *Services) tool.Handler[RollbackInput, RollbackOutput] {
	return func(ctx context.Context, in RollbackInput) (tool.Result[RollbackOutput], error) {
		scope, _, err := resolveScope(svc, ctx, scopeArgs{Project: in.ProjectID, User: in.UserID})
		if err != nil {
			return tool.Result[RollbackOutput]{}, fmt.Errorf("memory_rollback: %w", err)
		}
		if in.MemoryID == "" {
			return tool.Result[RollbackOutput]{}, fmt.Errorf("memory_rollback: memory_id must not be empty")
		}
		res, err := reconcile.Rollback(ctx, svc.Store, scope, in.MemoryID, svc.scopeInvalidator())
		if err != nil {
			return tool.Result[RollbackOutput]{}, fmt.Errorf("memory_rollback: %w", err)
		}
		var rec MemoryRecord
		if res.Memory != nil {
			rec = memoryToRecord(*res.Memory)
		} else {
			rec = MemoryRecord{ID: res.ID}
		}
		out := RollbackOutput{Memory: rec}
		return tool.Result[RollbackOutput]{
			Text:       fmt.Sprintf("Rolled back memory %s (restored status=%s)", rec.ID, rec.Status),
			Structured: out,
		}, nil
	}
}

func makeResolveHandler(svc *Services) tool.Handler[ResolveInput, ResolveOutput] {
	return func(ctx context.Context, in ResolveInput) (tool.Result[ResolveOutput], error) {
		scope, _, err := resolveScope(svc, ctx, scopeArgs{Project: in.ProjectID, User: in.UserID})
		if err != nil {
			return tool.Result[ResolveOutput]{}, fmt.Errorf("memory_resolve: %w", err)
		}
		if in.MemoryID == "" {
			return tool.Result[ResolveOutput]{}, fmt.Errorf("memory_resolve: memory_id must not be empty")
		}
		if in.Action != "confirm" && in.Action != "reject" {
			return tool.Result[ResolveOutput]{}, fmt.Errorf("memory_resolve: action must be confirm or reject")
		}
		res, err := reconcile.Resolve(ctx, svc.Store, scope, in.MemoryID, reconcile.ConfirmAction(in.Action), svc.scopeInvalidator())
		if err != nil {
			return tool.Result[ResolveOutput]{}, fmt.Errorf("memory_resolve: %w", err)
		}
		out := ResolveOutput{ID: res.ID, Status: res.Status}
		return tool.Result[ResolveOutput]{
			Text:       fmt.Sprintf("Resolved memory %s via %s → %s", res.ID, in.Action, res.Status),
			Structured: out,
		}, nil
	}
}

// ─── memory_topics ───────────────────────────────────────────────────────────

func makeTopicsHandler(svc *Services) tool.Handler[TopicsInput, TopicsOutput] {
	return func(ctx context.Context, in TopicsInput) (tool.Result[TopicsOutput], error) {
		scope, err := svc.ScopeFn(ctx)
		if err != nil {
			return tool.Result[TopicsOutput]{}, fmt.Errorf("memory_topics: resolve scope: %w", err)
		}
		// D-138 tenant guard (tenant-scoped management verb; no sub-tenant read to
		// narrow, ae2).
		if _, err := readMetaIdentity(ctx, scope.Tenant); err != nil {
			return tool.Result[TopicsOutput]{}, fmt.Errorf("memory_topics: %w", err)
		}

		switch in.Action {
		case "list", "":
			if svc.TopicSvc == nil {
				return tool.Result[TopicsOutput]{}, fmt.Errorf("memory_topics: topic service not available")
			}
			views, err := svc.TopicSvc.ActiveTopics(ctx, scope)
			if err != nil {
				return tool.Result[TopicsOutput]{}, fmt.Errorf("memory_topics: list: %w", err)
			}
			topicViews := make([]TopicView, len(views))
			for i, v := range views {
				topicViews[i] = TopicView{
					Key:         v.Key,
					Description: v.Description,
					Status:      v.Status,
					Pack:        v.Pack,
					Source:      v.Source,
				}
			}
			out := TopicsOutput{Topics: topicViews}
			return tool.Result[TopicsOutput]{
				Text:       fmt.Sprintf("Listed %d topic(s)", len(topicViews)),
				Structured: out,
			}, nil

		case "upsert":
			if svc.TopicSvc == nil {
				return tool.Result[TopicsOutput]{}, fmt.Errorf("memory_topics: topic service not available")
			}
			if len(in.Topics) == 0 {
				return tool.Result[TopicsOutput]{}, fmt.Errorf("memory_topics: topics array must not be empty for action=upsert")
			}
			// Route through the shared topics.Service so active|paused validation is
			// enforced identically to HTTP/SDK — one core, no per-surface drift
			// (D-071, Wave-B checkpoint; the prior inline build skipped status
			// validation).
			upserts := make([]topics.TopicUpsert, len(in.Topics))
			for i, t := range in.Topics {
				upserts[i] = topics.TopicUpsert{Key: t.Key, Description: t.Description, Status: t.Status}
			}
			n, err := svc.TopicSvc.Upsert(ctx, scope, upserts)
			if err != nil {
				return tool.Result[TopicsOutput]{}, fmt.Errorf("memory_topics: upsert: %w", err)
			}
			out := TopicsOutput{Upserted: n}
			return tool.Result[TopicsOutput]{
				Text:       fmt.Sprintf("Upserted %d topic(s)", n),
				Structured: out,
			}, nil

		case "delete":
			if svc.TopicSvc == nil {
				return tool.Result[TopicsOutput]{}, fmt.Errorf("memory_topics: topic service not available")
			}
			if in.Key == "" {
				return tool.Result[TopicsOutput]{}, fmt.Errorf("memory_topics: key must be set for action=delete")
			}
			if err := svc.TopicSvc.Delete(ctx, scope, in.Key); err != nil {
				return tool.Result[TopicsOutput]{}, fmt.Errorf("memory_topics: delete: %w", err)
			}
			out := TopicsOutput{Deleted: in.Key}
			return tool.Result[TopicsOutput]{
				Text:       fmt.Sprintf("Deleted topic %q", in.Key),
				Structured: out,
			}, nil

		default:
			return tool.Result[TopicsOutput]{}, fmt.Errorf("memory_topics: unknown action %q (want list|upsert|delete)", in.Action)
		}
	}
}

// ─── memory_flush (D-071) ──────────────────────────────────────────────────────

func makeFlushHandler(svc *Services) tool.Handler[FlushInput, FlushOutput] {
	return func(ctx context.Context, in FlushInput) (tool.Result[FlushOutput], error) {
		scope, err := svc.ScopeFn(ctx)
		if err != nil {
			return tool.Result[FlushOutput]{}, fmt.Errorf("memory_flush: resolve scope: %w", err)
		}
		// D-138 tenant guard (control verb; no sub-tenant read to narrow, ae2).
		if _, err := readMetaIdentity(ctx, scope.Tenant); err != nil {
			return tool.Result[FlushOutput]{}, fmt.Errorf("memory_flush: %w", err)
		}
		if in.Key == "" {
			return tool.Result[FlushOutput]{}, fmt.Errorf("memory_flush: key must not be empty")
		}
		trigger := in.Trigger
		switch trigger {
		case "", pipeline.TriggerExplicit:
			trigger = pipeline.TriggerExplicit
		case pipeline.TriggerSessionEnd:
			// valid
		default:
			return tool.Result[FlushOutput]{}, fmt.Errorf("memory_flush: trigger must be explicit or session_end")
		}
		flushed := false
		if svc.PipelineStage != nil {
			if err := svc.PipelineStage.FlushKey(ctx, scope, in.Key, trigger); err != nil {
				return tool.Result[FlushOutput]{}, fmt.Errorf("memory_flush: %w", err)
			}
			flushed = true
		}
		out := FlushOutput{Key: in.Key, Trigger: trigger, Flushed: flushed}
		return tool.Result[FlushOutput]{
			Text:       fmt.Sprintf("Flushed buffer %q (trigger=%s, flushed=%v)", in.Key, trigger, flushed),
			Structured: out,
		}, nil
	}
}

// ─── memory_branch (D-029, D-071) ──────────────────────────────────────────────

func makeBranchHandler(svc *Services) tool.Handler[BranchInput, BranchOutput] {
	return func(ctx context.Context, in BranchInput) (tool.Result[BranchOutput], error) {
		scope, effSession, err := resolveScope(svc, ctx, scopeArgs{Project: in.ProjectID, User: in.UserID, Session: in.SessionID})
		if err != nil {
			return tool.Result[BranchOutput]{}, fmt.Errorf("memory_branch: %w", err)
		}

		var out BranchOutput
		switch in.Action {
		case "fork":
			// Session-REPLACE (D-137/D-150): existing fork sink, never Scope.Session.
			id, err := pipeline.ForkBranch(ctx, svc.Store, scope, effSession, in.ParentBranchID)
			if err != nil {
				return tool.Result[BranchOutput]{}, fmt.Errorf("memory_branch: %w", err)
			}
			out = BranchOutput{BranchID: id, Status: "open"}
		case "merge":
			if err := pipeline.MergeBranch(ctx, svc.Store, scope, in.BranchID); err != nil {
				return tool.Result[BranchOutput]{}, fmt.Errorf("memory_branch: %w", err)
			}
			out = BranchOutput{BranchID: in.BranchID, Status: "merged"}
		case "discard":
			if err := pipeline.DiscardBranch(ctx, svc.Store, svc.PipelineStage, scope, in.BranchID); err != nil {
				return tool.Result[BranchOutput]{}, fmt.Errorf("memory_branch: %w", err)
			}
			out = BranchOutput{BranchID: in.BranchID, Status: "discarded"}
		default:
			return tool.Result[BranchOutput]{}, fmt.Errorf("memory_branch: unknown action %q (want fork|merge|discard)", in.Action)
		}
		return tool.Result[BranchOutput]{
			Text:       fmt.Sprintf("Branch %s: branch_id=%s status=%s", in.Action, out.BranchID, out.Status),
			Structured: out,
		}, nil
	}
}

// ─── memory_grants (Tier B — D-016, D-071) ─────────────────────────────────────

func grantGroupToWire(g store.Group) GrantGroup {
	return GrantGroup{ID: g.ID, TenantID: g.TenantID, Name: g.Name, CreatedAt: g.CreatedAt}
}

func grantMemberToWire(m store.GroupMember) GrantMember {
	return GrantMember{ID: m.ID, GroupID: m.GroupID, UserID: m.UserID, TenantID: m.TenantID, CreatedAt: m.CreatedAt}
}

func grantRecordToWire(g store.Grant) GrantRecord {
	return GrantRecord{
		ID: g.ID, TenantID: g.TenantID, ProjectID: g.ProjectID, UserID: g.UserID,
		SessionID: g.SessionID, GroupID: g.GroupID, Access: g.Access,
		TopicFilter: g.TopicFilter, KindFilter: g.KindFilter, ZoneCeiling: g.ZoneCeiling,
		RedactionProfile: g.RedactionProfile, RevokedAt: g.RevokedAt,
		CreatedAt: g.CreatedAt, UpdatedAt: g.UpdatedAt,
	}
}

func makeGrantsHandler(svc *Services) tool.Handler[GrantsInput, GrantsOutput] {
	return func(ctx context.Context, in GrantsInput) (tool.Result[GrantsOutput], error) {
		scope, err := svc.ScopeFn(ctx)
		if err != nil {
			return tool.Result[GrantsOutput]{}, fmt.Errorf("memory_grants: resolve scope: %w", err)
		}
		// D-138 tenant guard (admin verb; owner scope built from args, ae2).
		if _, err := readMetaIdentity(ctx, scope.Tenant); err != nil {
			return tool.Result[GrantsOutput]{}, fmt.Errorf("memory_grants: %w", err)
		}
		if svc.GrantsSvc == nil {
			return tool.Result[GrantsOutput]{}, fmt.Errorf("memory_grants: grants service not available")
		}
		tenantScope := identity.Scope{Tenant: scope.Tenant}

		switch in.Action {
		case "create_group":
			if in.Name == "" {
				return tool.Result[GrantsOutput]{}, fmt.Errorf("memory_grants: name is required for create_group")
			}
			g, err := svc.GrantsSvc.CreateGroup(ctx, tenantScope, in.Name)
			if err != nil {
				return tool.Result[GrantsOutput]{}, fmt.Errorf("memory_grants: create_group: %w", err)
			}
			wire := grantGroupToWire(*g)
			return grantsResult(GrantsOutput{Group: &wire}, fmt.Sprintf("Created group %s", wire.ID)), nil

		case "list_groups":
			grps, err := svc.GrantsSvc.ListGroups(ctx, tenantScope)
			if err != nil {
				return tool.Result[GrantsOutput]{}, fmt.Errorf("memory_grants: list_groups: %w", err)
			}
			out := GrantsOutput{Groups: make([]GrantGroup, len(grps))}
			for i, g := range grps {
				out.Groups[i] = grantGroupToWire(g)
			}
			return grantsResult(out, fmt.Sprintf("Listed %d group(s)", len(out.Groups))), nil

		case "add_member":
			if in.GroupID == "" || in.UserID == "" {
				return tool.Result[GrantsOutput]{}, fmt.Errorf("memory_grants: group_id and user_id are required for add_member")
			}
			m, err := svc.GrantsSvc.AddMember(ctx, tenantScope, in.GroupID, in.UserID)
			if err != nil {
				return tool.Result[GrantsOutput]{}, fmt.Errorf("memory_grants: add_member: %w", err)
			}
			wire := grantMemberToWire(*m)
			return grantsResult(GrantsOutput{Member: &wire}, fmt.Sprintf("Added member %s to group %s", in.UserID, in.GroupID)), nil

		case "remove_member":
			if in.GroupID == "" || in.UserID == "" {
				return tool.Result[GrantsOutput]{}, fmt.Errorf("memory_grants: group_id and user_id are required for remove_member")
			}
			if err := svc.GrantsSvc.RemoveMember(ctx, tenantScope, in.GroupID, in.UserID); err != nil {
				return tool.Result[GrantsOutput]{}, fmt.Errorf("memory_grants: remove_member: %w", err)
			}
			return grantsResult(GrantsOutput{Removed: true}, fmt.Sprintf("Removed member %s from group %s", in.UserID, in.GroupID)), nil

		case "list_members":
			if in.GroupID == "" {
				return tool.Result[GrantsOutput]{}, fmt.Errorf("memory_grants: group_id is required for list_members")
			}
			ms, err := svc.GrantsSvc.ListMembers(ctx, tenantScope, in.GroupID)
			if err != nil {
				return tool.Result[GrantsOutput]{}, fmt.Errorf("memory_grants: list_members: %w", err)
			}
			out := GrantsOutput{Members: make([]GrantMember, len(ms))}
			for i, m := range ms {
				out.Members[i] = grantMemberToWire(m)
			}
			return grantsResult(out, fmt.Sprintf("Listed %d member(s)", len(out.Members))), nil

		case "create_grant":
			if in.GroupID == "" {
				return tool.Result[GrantsOutput]{}, fmt.Errorf("memory_grants: group_id is required for create_grant")
			}
			if in.ZoneCeiling == "" {
				return tool.Result[GrantsOutput]{}, fmt.Errorf("memory_grants: zone_ceiling is required for create_grant (public or work)")
			}
			access := in.Access
			if access == "" {
				access = "read"
			}
			g, err := svc.GrantsSvc.CreateGrant(ctx, tenantScope, grants.CreateGrantInput{
				OwnerScope: identity.Scope{
					Tenant: tenantScope.Tenant, Project: in.ProjectID, User: in.UserID, Session: in.SessionID,
				},
				GroupID:          in.GroupID,
				Access:           access,
				TopicFilter:      in.TopicFilter,
				KindFilter:       in.KindFilter,
				ZoneCeiling:      in.ZoneCeiling,
				RedactionProfile: in.RedactionProfile,
			})
			if err != nil {
				return tool.Result[GrantsOutput]{}, fmt.Errorf("memory_grants: create_grant: %w", err)
			}
			wire := grantRecordToWire(*g)
			return grantsResult(GrantsOutput{Grant: &wire}, fmt.Sprintf("Created grant %s", wire.ID)), nil

		case "list_grants":
			gs, err := svc.GrantsSvc.ListGrants(ctx, tenantScope)
			if err != nil {
				return tool.Result[GrantsOutput]{}, fmt.Errorf("memory_grants: list_grants: %w", err)
			}
			out := GrantsOutput{Grants: make([]GrantRecord, len(gs))}
			for i, g := range gs {
				out.Grants[i] = grantRecordToWire(g)
			}
			return grantsResult(out, fmt.Sprintf("Listed %d grant(s)", len(out.Grants))), nil

		case "revoke_grant":
			if in.GrantID == "" {
				return tool.Result[GrantsOutput]{}, fmt.Errorf("memory_grants: grant_id is required for revoke_grant")
			}
			if err := svc.GrantsSvc.RevokeGrant(ctx, tenantScope, in.GrantID); err != nil {
				return tool.Result[GrantsOutput]{}, fmt.Errorf("memory_grants: revoke_grant: %w", err)
			}
			return grantsResult(GrantsOutput{Revoked: in.GrantID}, fmt.Sprintf("Revoked grant %s", in.GrantID)), nil

		default:
			return tool.Result[GrantsOutput]{}, fmt.Errorf("memory_grants: unknown action %q", in.Action)
		}
	}
}

func grantsResult(out GrantsOutput, text string) tool.Result[GrantsOutput] {
	return tool.Result[GrantsOutput]{Text: text, Structured: out}
}

// ─── memory_agent_policy (Phase ae1 — D-135/D-146/D-151) ───────────────────────

func agentPolicyToWire(p store.AgentPolicy) AgentPolicyRecord {
	return AgentPolicyRecord{
		AgentID: p.AgentID, AllowTopics: p.AllowTopics, DenyTopics: p.DenyTopics,
		CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt,
	}
}

// makeAgentPolicyHandler implements memory_agent_policy: op-dispatched
// (create|get|list|delete) admin over the (tenant_id, agent_id) -> {allow, deny}
// topic-key binding (D-135/D-146/D-151). Policy-admin tier (mirrors
// makeGrantsHandler): reachable on {HTTP, MCP} only, never the single-user SDK
// (D-067). The CORE is svc.Retriever's PutAgentPolicy/DeleteAgentPolicy (which
// also invalidate the affected agent's cached reads, §6 blocking #2) and
// GetAgentPolicy/ListAgentPolicies (pure reads) — validation lives in the
// TopicViewStore drivers, proven identically by conformance, so this handler is a
// thin caller.
func makeAgentPolicyHandler(svc *Services) tool.Handler[AgentPolicyInput, AgentPolicyOutput] {
	return func(ctx context.Context, in AgentPolicyInput) (tool.Result[AgentPolicyOutput], error) {
		scope, err := svc.ScopeFn(ctx)
		if err != nil {
			return tool.Result[AgentPolicyOutput]{}, fmt.Errorf("memory_agent_policy: resolve scope: %w", err)
		}
		// D-138 tenant guard (admin verb, tenant-scoped like memory_grants; not
		// enumerated in the ae2 plan's handler table because it landed with ae1
		// after the table was authored — added here so the guard covers every
		// handler per AC-3, an ae2 as-built deviation).
		if _, err := readMetaIdentity(ctx, scope.Tenant); err != nil {
			return tool.Result[AgentPolicyOutput]{}, fmt.Errorf("memory_agent_policy: %w", err)
		}
		if svc.Retriever == nil {
			return tool.Result[AgentPolicyOutput]{}, fmt.Errorf("memory_agent_policy: retriever not available")
		}
		tenantScope := identity.Scope{Tenant: scope.Tenant}

		switch in.Action {
		case "create":
			if in.AgentID == "" {
				return tool.Result[AgentPolicyOutput]{}, fmt.Errorf("memory_agent_policy: agent_id is required for create")
			}
			p := store.AgentPolicy{AgentID: in.AgentID, AllowTopics: in.AllowTopics, DenyTopics: in.DenyTopics}
			if err := svc.Retriever.PutAgentPolicy(ctx, tenantScope, p); err != nil {
				return tool.Result[AgentPolicyOutput]{}, fmt.Errorf("memory_agent_policy: create: %w", err)
			}
			got, err := svc.Retriever.GetAgentPolicy(ctx, tenantScope, in.AgentID)
			if err != nil {
				return tool.Result[AgentPolicyOutput]{}, fmt.Errorf("memory_agent_policy: create (read-back): %w", err)
			}
			wire := agentPolicyToWire(*got)
			return agentPolicyResult(AgentPolicyOutput{Policy: &wire}, fmt.Sprintf("Bound agent %s", in.AgentID)), nil

		case "get":
			if in.AgentID == "" {
				return tool.Result[AgentPolicyOutput]{}, fmt.Errorf("memory_agent_policy: agent_id is required for get")
			}
			p, err := svc.Retriever.GetAgentPolicy(ctx, tenantScope, in.AgentID)
			if err != nil {
				return tool.Result[AgentPolicyOutput]{}, fmt.Errorf("memory_agent_policy: get: %w", err)
			}
			wire := agentPolicyToWire(*p)
			return agentPolicyResult(AgentPolicyOutput{Policy: &wire}, fmt.Sprintf("Agent %s binding", in.AgentID)), nil

		case "list":
			list, err := svc.Retriever.ListAgentPolicies(ctx, tenantScope)
			if err != nil {
				return tool.Result[AgentPolicyOutput]{}, fmt.Errorf("memory_agent_policy: list: %w", err)
			}
			out := AgentPolicyOutput{Policies: make([]AgentPolicyRecord, len(list))}
			for i, p := range list {
				out.Policies[i] = agentPolicyToWire(p)
			}
			return agentPolicyResult(out, fmt.Sprintf("Listed %d agent binding(s)", len(out.Policies))), nil

		case "delete":
			if in.AgentID == "" {
				return tool.Result[AgentPolicyOutput]{}, fmt.Errorf("memory_agent_policy: agent_id is required for delete")
			}
			if err := svc.Retriever.DeleteAgentPolicy(ctx, tenantScope, in.AgentID); err != nil {
				return tool.Result[AgentPolicyOutput]{}, fmt.Errorf("memory_agent_policy: delete: %w", err)
			}
			return agentPolicyResult(AgentPolicyOutput{Deleted: in.AgentID}, fmt.Sprintf("Unbound agent %s", in.AgentID)), nil

		default:
			return tool.Result[AgentPolicyOutput]{}, fmt.Errorf("memory_agent_policy: unknown action %q (want create|get|list|delete)", in.Action)
		}
	}
}

func agentPolicyResult(out AgentPolicyOutput, text string) tool.Result[AgentPolicyOutput] {
	return tool.Result[AgentPolicyOutput]{Text: text, Structured: out}
}

// makeEpisodesHandler implements memory_episodes (RFC §6b, D-080): the
// deterministic, LLM-free episodic-retrieval read (mirrors GET /v1/episodes + the
// embedded SDK Episodes). ID returns one episode; else a list narrowed by
// session/window. Scope resolved via svc.ScopeFn.
func makeEpisodesHandler(svc *Services) tool.Handler[EpisodesInput, EpisodesOutput] {
	return func(ctx context.Context, in EpisodesInput) (tool.Result[EpisodesOutput], error) {
		scope, effSession, err := resolveScope(svc, ctx, scopeArgs{Project: in.ProjectID, User: in.UserID, Session: in.SessionID})
		if err != nil {
			return tool.Result[EpisodesOutput]{}, fmt.Errorf("memory_episodes: %w", err)
		}
		var out EpisodesOutput
		switch {
		case in.ID != "":
			v, gerr := episodes.Get(ctx, svc.Store, scope, in.ID)
			if errors.Is(gerr, store.ErrNotFound) {
				out.Episodes = []EpisodeItem{}
			} else if gerr != nil {
				return tool.Result[EpisodesOutput]{}, fmt.Errorf("memory_episodes: %w", gerr)
			} else {
				out.Episodes = []EpisodeItem{episodeToItem(*v)}
			}
		case in.SimilarTo != "":
			// Vector-rank the scope's episodes by narrative similarity (§6b, D-082).
			// Degrades to an empty+degraded envelope when the gateway is down.
			if svc.Retriever == nil {
				out.Episodes = []EpisodeItem{}
				out.Degraded = true
				break
			}
			views, degraded, serr := episodes.Similar(ctx, svc.Store, svc.Retriever, scope, in.SimilarTo, in.K)
			if serr != nil {
				return tool.Result[EpisodesOutput]{}, fmt.Errorf("memory_episodes: %w", serr)
			}
			out.Episodes = make([]EpisodeItem, 0, len(views))
			for _, v := range views {
				out.Episodes = append(out.Episodes, episodeToItem(v))
			}
			out.Degraded = degraded
		case in.ArcOf != "":
			// Cross-session arc of an episode (§6b threading, D-081).
			views, aerr := episodes.Arc(ctx, svc.Store, scope, in.ArcOf)
			if aerr != nil {
				return tool.Result[EpisodesOutput]{}, fmt.Errorf("memory_episodes: %w", aerr)
			}
			out.Episodes = make([]EpisodeItem, 0, len(views))
			for _, v := range views {
				out.Episodes = append(out.Episodes, episodeToItem(v))
			}
		default:
			// Session-REPLACE (D-137/D-150): existing window/session arg sink, never
			// Scope.Session.
			res, lerr := episodes.List(ctx, svc.Store, scope, episodes.ListOptions{
				Limit: in.Limit, Cursor: in.Cursor, SessionID: effSession, From: in.From, Until: in.Until,
			})
			if lerr != nil {
				return tool.Result[EpisodesOutput]{}, fmt.Errorf("memory_episodes: %w", lerr)
			}
			out.Episodes = make([]EpisodeItem, 0, len(res.Episodes))
			for _, v := range res.Episodes {
				out.Episodes = append(out.Episodes, episodeToItem(v))
			}
			out.NextCursor = res.NextCursor
		}
		return tool.Result[EpisodesOutput]{
			Text:       fmt.Sprintf("Episodes: %d returned", len(out.Episodes)),
			Structured: out,
		}, nil
	}
}

func episodeToItem(v episodes.EpisodeView) EpisodeItem {
	return EpisodeItem{
		ID: v.ID, SessionID: v.SessionID, Title: v.Title, Status: v.Status, Outcome: v.Outcome,
		StartedAt: v.StartedAt, EndedAt: v.EndedAt, NarrativeMemoryID: v.NarrativeMemoryID, Narrative: v.Narrative,
		Score: v.Score,
	}
}

// makeBrowseHandler implements memory_browse (ae5, D-143): the deterministic,
// gateway-free scoped walk over a scope's memories (mirrors GET /v1/memories +
// the embedded SDK Browse). Thin caller over retrieval.Browse — the one core
// (D-067/D-073). Scope resolved via svc.ScopeFn, narrowed by ProjectID/UserID
// (D-125).
func makeBrowseHandler(svc *Services) tool.Handler[BrowseInput, BrowseOutput] {
	return func(ctx context.Context, in BrowseInput) (tool.Result[BrowseOutput], error) {
		// BrowseOptions has no session dimension (ae5), so scopeArgs.Session is
		// left zero.
		scope, _, err := resolveScope(svc, ctx, scopeArgs{Project: in.ProjectID, User: in.UserID})
		if err != nil {
			return tool.Result[BrowseOutput]{}, fmt.Errorf("memory_browse: %w", err)
		}

		mode, perr := retrieval.ParseBrowseMode(in.Mode)
		if perr != nil {
			return tool.Result[BrowseOutput]{}, fmt.Errorf("memory_browse: %w", perr)
		}

		res, berr := retrieval.Browse(ctx, svc.Store, scope, retrieval.BrowseOptions{
			Mode: mode, Limit: in.Limit, Cursor: in.Cursor, DefaultLimit: svc.BrowseDefaultLimit,
		})
		if berr != nil {
			return tool.Result[BrowseOutput]{}, fmt.Errorf("memory_browse: %w", berr)
		}

		out := BrowseOutput{Memories: make([]BrowseMemoryItem, 0, len(res.Memories)), NextCursor: res.NextCursor}
		for i := range res.Memories {
			out.Memories = append(out.Memories, memoryToBrowseItem(&res.Memories[i]))
		}
		return tool.Result[BrowseOutput]{
			Text:       fmt.Sprintf("Browse: %d memories returned", len(out.Memories)),
			Structured: out,
		}, nil
	}
}

// memoryToBrowseItem converts a *store.Memory to the memory_browse wire item.
func memoryToBrowseItem(m *store.Memory) BrowseMemoryItem {
	return BrowseMemoryItem{
		ID: m.ID, Kind: m.Kind, Content: m.Content, Context: m.Context, Status: m.Status,
		Importance: m.Importance, Confidence: m.Confidence, TrustSource: m.TrustSource,
		MatchCount: m.MatchCount, InjectCount: m.InjectCount, UseCount: m.UseCount,
		SaveCount: m.SaveCount, FailCount: m.FailCount, NoiseCount: m.NoiseCount,
		Stability: m.Stability, ValidFrom: m.ValidFrom, ValidUntil: m.ValidUntil,
		EpisodeID: m.EpisodeID, SupersedesID: m.SupersedesID, SupersededByID: m.SupersededByID,
		PrivacyZone: m.PrivacyZone, ContentHash: m.ContentHash,
		CreatedAt: m.CreatedAt, UpdatedAt: m.UpdatedAt,
	}
}

// makeCausalHandler implements memory_causal (RFC §5.6/§6b, D-083): the
// deterministic, gateway-free why-traversal (mirrors GET /v1/causal + the embedded
// SDK Causal). Scope resolved via svc.ScopeFn.
func makeCausalHandler(svc *Services) tool.Handler[CausalInput, CausalOutput] {
	return func(ctx context.Context, in CausalInput) (tool.Result[CausalOutput], error) {
		scope, _, err := resolveScope(svc, ctx, scopeArgs{Project: in.ProjectID, User: in.UserID})
		if err != nil {
			return tool.Result[CausalOutput]{}, fmt.Errorf("memory_causal: %w", err)
		}
		if in.MemoryID == "" {
			return tool.Result[CausalOutput]{}, fmt.Errorf("memory_causal: memory_id is required")
		}
		g, terr := causal.Traverse(ctx, svc.Store, scope, in.MemoryID, causal.Direction(in.Direction), in.Depth)
		if terr != nil {
			return tool.Result[CausalOutput]{}, fmt.Errorf("memory_causal: %w", terr)
		}
		out := causalGraphToOutput(g)
		return tool.Result[CausalOutput]{
			Text:       fmt.Sprintf("Causal graph: %d nodes, %d edges", len(out.Nodes), len(out.Edges)),
			Structured: out,
		}, nil
	}
}

func causalGraphToOutput(g causal.Graph) CausalOutput {
	out := CausalOutput{Root: g.Root, Truncated: g.Truncated,
		Nodes: make([]CausalNodeItem, 0, len(g.Nodes)), Edges: make([]CausalEdgeItem, 0, len(g.Edges))}
	for _, n := range g.Nodes {
		cn := CausalNodeItem{MemoryID: n.MemoryID, Kind: n.Kind, Content: n.Content, Context: n.Context, EpisodeID: n.EpisodeID}
		for _, p := range n.Provenance {
			cn.Provenance = append(cn.Provenance, CausalProvRefItem{RecordID: p.RecordID, SpanStart: p.SpanStart, SpanEnd: p.SpanEnd})
		}
		out.Nodes = append(out.Nodes, cn)
	}
	for _, e := range g.Edges {
		out.Edges = append(out.Edges, CausalEdgeItem{From: e.From, To: e.To, Type: e.Type, Confidence: e.Confidence})
	}
	return out
}

// ─── memory_verify / memory_review (Phase 25, D-084) ──────────────────────────

// makeVerifyHandler implements memory_verify (RFC §6c): resolve the claim's citation
// handles and run a schema-constrained gateway entailment check. Degrades to unclear
// when the gateway is unreachable (D-036). Mirrors POST /v1/verify + SDK Verify.
func makeVerifyHandler(svc *Services) tool.Handler[VerifyInput, VerifyOutput] {
	return func(ctx context.Context, in VerifyInput) (tool.Result[VerifyOutput], error) {
		scope, _, err := resolveScope(svc, ctx, scopeArgs{Project: in.ProjectID, User: in.UserID})
		if err != nil {
			return tool.Result[VerifyOutput]{}, fmt.Errorf("memory_verify: %w", err)
		}
		if in.Claim == "" {
			return tool.Result[VerifyOutput]{}, fmt.Errorf("memory_verify: claim is required")
		}
		v, err := trust.VerifyClaim(ctx, svc.Store, svc.Gateway, scope, in.Claim, in.Citations)
		if err != nil {
			return tool.Result[VerifyOutput]{}, fmt.Errorf("memory_verify: %w", err)
		}
		out := VerifyOutput{Verdict: v.Verdict, Confidence: v.Confidence, Explanation: v.Explanation, Degraded: v.Degraded}
		return tool.Result[VerifyOutput]{
			Text:       fmt.Sprintf("Verify: %s (confidence %.2f)", v.Verdict, v.Confidence),
			Structured: out,
		}, nil
	}
}

// makeReviewHandler implements memory_review (RFC §6c): list the scope's pending_review
// memories or approve/reject one. Mirrors GET /v1/review + POST /v1/review/{id} + SDK Review.
func makeReviewHandler(svc *Services) tool.Handler[ReviewInput, ReviewOutput] {
	return func(ctx context.Context, in ReviewInput) (tool.Result[ReviewOutput], error) {
		scope, _, err := resolveScope(svc, ctx, scopeArgs{Project: in.ProjectID, User: in.UserID})
		if err != nil {
			return tool.Result[ReviewOutput]{}, fmt.Errorf("memory_review: %w", err)
		}
		switch in.Action {
		case "list":
			mems, next, lerr := trust.ListPending(ctx, svc.Store, scope, in.Limit, in.Cursor)
			if lerr != nil {
				return tool.Result[ReviewOutput]{}, fmt.Errorf("memory_review: %w", lerr)
			}
			out := ReviewOutput{Items: make([]ReviewItem, 0, len(mems)), NextCursor: next}
			for _, m := range mems {
				out.Items = append(out.Items, ReviewItem{ID: m.ID, Kind: m.Kind, Content: m.Content, Context: m.Context, CreatedAt: m.CreatedAt})
			}
			return tool.Result[ReviewOutput]{Text: fmt.Sprintf("Review queue: %d pending", len(out.Items)), Structured: out}, nil
		case "approve", "reject":
			if in.ID == "" {
				return tool.Result[ReviewOutput]{}, fmt.Errorf("memory_review: id required for action=%s", in.Action)
			}
			res, rerr := trust.Resolve(ctx, svc.Store, scope, in.ID, trust.ReviewAction(in.Action), svc.scopeInvalidator())
			if rerr != nil {
				return tool.Result[ReviewOutput]{}, fmt.Errorf("memory_review: %w", rerr)
			}
			return tool.Result[ReviewOutput]{
				Text:       fmt.Sprintf("Review %s: %s → %s", in.Action, res.ID, res.Status),
				Structured: ReviewOutput{ID: res.ID, Status: res.Status},
			}, nil
		default:
			return tool.Result[ReviewOutput]{}, fmt.Errorf("memory_review: action must be list|approve|reject")
		}
	}
}

// ─── memory_trace (Phase 26, D-086) ───────────────────────────────────────────

// makeTraceHandler implements memory_trace (RFC §6c): reconstruct the reasoning trace
// for a response_id from the day-one tables and return it as an optionally
// ed25519-signed bundle. Mirrors GET /v1/traces/{response_id} + SDK Trace.
func makeTraceHandler(svc *Services) tool.Handler[TraceInput, traces.Bundle] {
	return func(ctx context.Context, in TraceInput) (tool.Result[traces.Bundle], error) {
		scope, _, err := resolveScope(svc, ctx, scopeArgs{Project: in.ProjectID, User: in.UserID})
		if err != nil {
			return tool.Result[traces.Bundle]{}, fmt.Errorf("memory_trace: %w", err)
		}
		if in.ResponseID == "" {
			return tool.Result[traces.Bundle]{}, fmt.Errorf("memory_trace: response_id is required")
		}
		tr, rerr := traces.Reconstruct(ctx, svc.Store, scope, in.ResponseID, time.Now().UnixMilli())
		if rerr != nil {
			return tool.Result[traces.Bundle]{}, fmt.Errorf("memory_trace: %w", rerr)
		}
		bundle, serr := traces.Sign(tr, svc.TraceSigner)
		if serr != nil {
			return tool.Result[traces.Bundle]{}, fmt.Errorf("memory_trace: %w", serr)
		}
		return tool.Result[traces.Bundle]{
			Text:       fmt.Sprintf("Trace: %d items, %d verdicts (signed=%v)", len(bundle.Trace.Items), len(bundle.Trace.Verdicts), bundle.Signed),
			Structured: bundle,
		}, nil
	}
}

// ─── memory_suggestions (Phase 27, D-087) ──────────────────────────────────────

// makeSuggestionsHandler implements memory_suggestions (RFC §6d): the proactive
// pull (action=list evaluates+offers) and feedback (accept|dismiss resolve an
// offer). Single-user tier; mirrors GET /v1/suggestions + POST /v1/suggestions/{id}
// and the embedded SDK Suggestions. Scope resolved via svc.ScopeFn.
func makeSuggestionsHandler(svc *Services) tool.Handler[SuggestionsInput, SuggestionsOutput] {
	return func(ctx context.Context, in SuggestionsInput) (tool.Result[SuggestionsOutput], error) {
		// SuggestionsInput has no user_id/project_id arg — the effective user comes
		// from _meta/JWT alone (ae2 table caveat, carried forward).
		scope, effSession, err := resolveScope(svc, ctx, scopeArgs{Session: in.SessionID})
		if err != nil {
			return tool.Result[SuggestionsOutput]{}, fmt.Errorf("memory_suggestions: %w", err)
		}
		action := in.Action
		if action == "" {
			action = "list"
		}
		switch action {
		case "list":
			cfg, rerr := proactive.Resolve(ctx, svc.Store.ScopeSettings(), scope, proactiveProfileDefault(svc.Profile))
			if rerr != nil {
				return tool.Result[SuggestionsOutput]{}, fmt.Errorf("memory_suggestions: %w", rerr)
			}
			// Session-REPLACE (D-137/D-150): existing Evaluate sink, never Scope.Session.
			offers, degraded, eerr := proactive.Evaluate(ctx, svc.Store, svc.Retriever, scope, effSession, in.Query, cfg, time.Now().UnixMilli())
			if eerr != nil {
				return tool.Result[SuggestionsOutput]{}, fmt.Errorf("memory_suggestions: %w", eerr)
			}
			out := SuggestionsOutput{Suggestions: make([]SuggestionItem, 0, len(offers)), Degraded: degraded}
			for _, o := range offers {
				out.Suggestions = append(out.Suggestions, SuggestionItem{
					ID: o.ID, TriggerKind: o.TriggerKind, MemoryID: o.MemoryID,
					EpisodeID: o.EpisodeID, Title: o.Title, Content: o.Content, Score: o.Score,
				})
			}
			return tool.Result[SuggestionsOutput]{
				Text: fmt.Sprintf("Suggestions: %d offered (degraded=%v)", len(out.Suggestions), degraded), Structured: out,
			}, nil
		case "accept", "dismiss":
			if in.ID == "" {
				return tool.Result[SuggestionsOutput]{}, fmt.Errorf("memory_suggestions: id is required for %s", action)
			}
			sug, rerr := proactive.ResolveOffer(ctx, svc.Store, scope, in.ID, action, time.Now().UnixMilli())
			if errors.Is(rerr, store.ErrNotPending) || errors.Is(rerr, store.ErrNotFound) {
				return tool.Result[SuggestionsOutput]{}, fmt.Errorf("memory_suggestions: suggestion not found or already resolved")
			}
			if rerr != nil {
				return tool.Result[SuggestionsOutput]{}, fmt.Errorf("memory_suggestions: %w", rerr)
			}
			out := SuggestionsOutput{Suggestions: []SuggestionItem{}, ID: sug.ID, Status: sug.Status}
			return tool.Result[SuggestionsOutput]{Text: fmt.Sprintf("Suggestion %s: %s", sug.ID, sug.Status), Structured: out}, nil
		default:
			return tool.Result[SuggestionsOutput]{}, fmt.Errorf("memory_suggestions: action must be list, accept, or dismiss")
		}
	}
}

// ─── memory_proactive_config (Phase 27, D-087) ──────────────────────────────────

// makeProactiveConfigHandler implements memory_proactive_config (admin tier):
// action=get returns the scope's effective governance; action=set stores the
// override. Mirrors GET/PUT /v1/admin/proactive. Scope resolved via svc.ScopeFn,
// refined by User/Project.
func makeProactiveConfigHandler(svc *Services) tool.Handler[ProactiveConfigInput, ProactiveConfigOutput] {
	return func(ctx context.Context, in ProactiveConfigInput) (tool.Result[ProactiveConfigOutput], error) {
		base, err := svc.ScopeFn(ctx)
		if err != nil {
			return tool.Result[ProactiveConfigOutput]{}, fmt.Errorf("memory_proactive_config: resolve scope: %w", err)
		}
		// D-138 tenant guard (runs on BOTH actions); the identity is discarded here
		// because action=set (a persist) is never _meta-narrowed — see the ae2
		// plan's memory_proactive_config caveat. _meta.user intake is applied only
		// inside the "get" arm below, via resolveScope, just before proactive.Resolve.
		if _, err := readMetaIdentity(ctx, base.Tenant); err != nil {
			return tool.Result[ProactiveConfigOutput]{}, fmt.Errorf("memory_proactive_config: %w", err)
		}
		// Write-path (action=set) scope stays arg-only per the ae2 D-138 caveat —
		// a persist is never _meta/JWT-narrowed. The get arm narrows via
		// resolveScope below. Built field-by-field (not a read-scope literal) so it
		// is not a resolver-bypass (ae8 AC-9).
		scope := identity.Scope{Tenant: base.Tenant, Project: in.Project}
		scope.User = in.User
		action := in.Action
		if action == "" {
			action = "get"
		}
		var cfg proactive.Config
		var rerr error
		switch action {
		case "set":
			// PATCH semantics (D-067 core): only the provided fields overwrite, so a
			// one-field set never zero-wipes the rest of the config.
			patch := proactive.ConfigPatch{Enabled: in.Enabled, Threshold: in.Threshold, Budget: in.Budget, Classes: in.Classes}
			cfg, rerr = proactive.WriteGovernance(ctx, svc.Store.ScopeSettings(), scope, proactiveProfileDefault(svc.Profile), patch, time.Now().UnixMilli())
		case "get":
			// action=get IS a read — routed through the ONE cross-surface resolver
			// (ae8, D-148), unlike the arg-only "set" persist above.
			readScope, _, rerr2 := resolveScope(svc, ctx, scopeArgs{Project: in.Project, User: in.User})
			if rerr2 != nil {
				return tool.Result[ProactiveConfigOutput]{}, fmt.Errorf("memory_proactive_config: %w", rerr2)
			}
			cfg, rerr = proactive.Resolve(ctx, svc.Store.ScopeSettings(), readScope, proactiveProfileDefault(svc.Profile))
		default:
			return tool.Result[ProactiveConfigOutput]{}, fmt.Errorf("memory_proactive_config: action must be get or set")
		}
		if rerr != nil {
			return tool.Result[ProactiveConfigOutput]{}, fmt.Errorf("memory_proactive_config: %w", rerr)
		}
		classes := cfg.Classes
		if classes == nil {
			classes = map[string]bool{}
		}
		out := ProactiveConfigOutput{Enabled: cfg.Enabled, Threshold: cfg.Threshold, Budget: cfg.Budget, Classes: classes}
		return tool.Result[ProactiveConfigOutput]{
			Text: fmt.Sprintf("Proactive: enabled=%v threshold=%.2f budget=%d", cfg.Enabled, cfg.Threshold, cfg.Budget), Structured: out,
		}, nil
	}
}

// proactiveProfileDefault maps the profile's proactive defaults onto proactive.Config.
func proactiveProfileDefault(profile string) proactive.Config {
	pc := config.ProactiveConfigForProfile(profile)
	return proactive.Config{Enabled: pc.Enabled, Threshold: pc.Threshold, Budget: pc.Budget, Classes: pc.Classes}
}
