package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/hurtener/dockyard/runtime/tool"
	"github.com/oklog/ulid/v2"

	"github.com/hurtener/stowage/internal/grants"
	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/pipeline"
	"github.com/hurtener/stowage/internal/reconcile"
	"github.com/hurtener/stowage/internal/records"
	"github.com/hurtener/stowage/internal/retrieval"
	"github.com/hurtener/stowage/internal/store"
)

// ─── memory_ingest ────────────────────────────────────────────────────────────

func makeIngestHandler(svc *Services) tool.Handler[IngestInput, IngestOutput] {
	return func(ctx context.Context, in IngestInput) (tool.Result[IngestOutput], error) {
		scope, err := svc.ScopeFn(ctx)
		if err != nil {
			return tool.Result[IngestOutput]{}, fmt.Errorf("memory_ingest: resolve scope: %w", err)
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
		scope, err := svc.ScopeFn(ctx)
		if err != nil {
			return tool.Result[RetrieveOutput]{}, fmt.Errorf("memory_retrieve: resolve scope: %w", err)
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
			SessionID:    in.SessionID,
			Debug:        in.Debug,
			ResponseID:   in.ResponseID,
			Profile:      in.Profile,
		})
		if err != nil {
			return tool.Result[RetrieveOutput]{}, fmt.Errorf("memory_retrieve: %w", err)
		}

		items := make([]RetrieveItem, len(resp.Items))
		for i, it := range resp.Items {
			ri := RetrieveItem{
				ID:       it.Memory.ID,
				Kind:     it.Memory.Kind,
				Content:  it.Memory.Content,
				Context:  it.Memory.Context,
				Score:    it.Score,
				Citation: it.Citation,
			}
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
			Degraded:       resp.Degraded,
			DegradedRerank: resp.DegradedRerank,
			CacheHit:       resp.CacheHit,
			API:            resp.API,
		}
		return tool.Result[RetrieveOutput]{
			Text:       fmt.Sprintf("Retrieved %d item(s); response_id=%s", len(items), resp.ResponseID),
			Structured: out,
		}, nil
	}
}

// ─── memory_playbook ─────────────────────────────────────────────────────────

func makePlaybookHandler(_ *Services) tool.Handler[PlaybookInput, PlaybookOutput] {
	return func(_ context.Context, _ PlaybookInput) (tool.Result[PlaybookOutput], error) {
		// Stub: lands in Phase 17.
		out := PlaybookOutput{Error: "memory_playbook: not implemented — lands in Phase 17"}
		return tool.Result[PlaybookOutput]{
			Text:       out.Error,
			Structured: out,
		}, nil
	}
}

// ─── memory_drilldown ────────────────────────────────────────────────────────

func makeDrilldownHandler(svc *Services) tool.Handler[DrilldownInput, DrilldownOutput] {
	return func(ctx context.Context, in DrilldownInput) (tool.Result[DrilldownOutput], error) {
		scope, err := svc.ScopeFn(ctx)
		if err != nil {
			return tool.Result[DrilldownOutput]{}, fmt.Errorf("memory_drilldown: resolve scope: %w", err)
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
		scope, err := svc.ScopeFn(ctx)
		if err != nil {
			return tool.Result[FeedbackOutput]{}, fmt.Errorf("memory_feedback: resolve scope: %w", err)
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

// ─── memory_assert ────────────────────────────────────────────────────────────

func makeAssertHandler(svc *Services) tool.Handler[AssertInput, AssertOutput] {
	return func(ctx context.Context, in AssertInput) (tool.Result[AssertOutput], error) {
		scope, err := svc.ScopeFn(ctx)
		if err != nil {
			return tool.Result[AssertOutput]{}, fmt.Errorf("memory_assert: resolve scope: %w", err)
		}

		// Shared assert core (D-071) — identical logic to the embedded SDK Assert.
		res, err := reconcile.Assert(ctx, svc.Store, scope, reconcile.AssertParams{
			Action:   in.Action,
			MemoryID: in.MemoryID,
			Content:  in.Content,
			Kind:     in.Kind,
			Context:  in.Context,
		})
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
		scope, err := svc.ScopeFn(ctx)
		if err != nil {
			return tool.Result[GetOutput]{}, fmt.Errorf("memory_get: resolve scope: %w", err)
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
		scope, err := svc.ScopeFn(ctx)
		if err != nil {
			return tool.Result[RollbackOutput]{}, fmt.Errorf("memory_rollback: resolve scope: %w", err)
		}
		if in.MemoryID == "" {
			return tool.Result[RollbackOutput]{}, fmt.Errorf("memory_rollback: memory_id must not be empty")
		}
		res, err := reconcile.Rollback(ctx, svc.Store, scope, in.MemoryID)
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
		scope, err := svc.ScopeFn(ctx)
		if err != nil {
			return tool.Result[ResolveOutput]{}, fmt.Errorf("memory_resolve: resolve scope: %w", err)
		}
		if in.MemoryID == "" {
			return tool.Result[ResolveOutput]{}, fmt.Errorf("memory_resolve: memory_id must not be empty")
		}
		if in.Action != "confirm" && in.Action != "reject" {
			return tool.Result[ResolveOutput]{}, fmt.Errorf("memory_resolve: action must be confirm or reject")
		}
		res, err := reconcile.Resolve(ctx, svc.Store, scope, in.MemoryID, reconcile.ConfirmAction(in.Action))
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
			if len(in.Topics) == 0 {
				return tool.Result[TopicsOutput]{}, fmt.Errorf("memory_topics: topics array must not be empty for action=upsert")
			}
			now := time.Now().UnixMilli()
			for i, t := range in.Topics {
				if t.Key == "" {
					return tool.Result[TopicsOutput]{}, fmt.Errorf("memory_topics: item[%d]: key must not be empty", i)
				}
				status := t.Status
				if status == "" {
					status = "active"
				}
				st := store.Topic{
					ID:          ulid.Make().String(),
					TenantID:    scope.Tenant,
					Key:         t.Key,
					Description: t.Description,
					Status:      status,
					CreatedAt:   now,
					UpdatedAt:   now,
				}
				if err := svc.Store.Topics().Upsert(ctx, scope, st); err != nil {
					return tool.Result[TopicsOutput]{}, fmt.Errorf("memory_topics: upsert item[%d]: %w", i, err)
				}
			}
			out := TopicsOutput{Upserted: len(in.Topics)}
			return tool.Result[TopicsOutput]{
				Text:       fmt.Sprintf("Upserted %d topic(s)", len(in.Topics)),
				Structured: out,
			}, nil

		case "delete":
			if in.Key == "" {
				return tool.Result[TopicsOutput]{}, fmt.Errorf("memory_topics: key must be set for action=delete")
			}
			if err := svc.Store.Topics().Delete(ctx, scope, in.Key); err != nil {
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
		scope, err := svc.ScopeFn(ctx)
		if err != nil {
			return tool.Result[BranchOutput]{}, fmt.Errorf("memory_branch: resolve scope: %w", err)
		}

		var out BranchOutput
		switch in.Action {
		case "fork":
			id, err := pipeline.ForkBranch(ctx, svc.Store, scope, in.SessionID, in.ParentBranchID)
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
