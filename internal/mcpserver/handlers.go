package mcpserver

import (
	"context"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/hurtener/dockyard/runtime/tool"
	"github.com/oklog/ulid/v2"

	"github.com/hurtener/stowage/internal/pipeline"
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
			rec, err := records.New(records.Input{
				TenantID:      scope.Tenant,
				ProjectID:     item.ProjectID,
				UserID:        item.UserID,
				SessionID:     item.SessionID,
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

		// Non-blocking pipeline enqueue.
		allEnqueued := true
		for _, si := range stamped {
			if svc.PipelineIn == nil {
				allEnqueued = false
				continue
			}
			select {
			case svc.PipelineIn <- pipeline.Item{
				RecordID:  si.rec.ID,
				TenantID:  scope.Tenant,
				BufferKey: si.bufferKey,
				SessionID: si.rec.SessionID,
				BranchID:  si.rec.BranchID,
			}:
			default:
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
				Excerpt:    clampExcerpt(r.Content, p.SpanStart, p.SpanEnd),
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

// clampExcerpt returns content[s:e] with bounds clamped and UTF-8 safe.
func clampExcerpt(content string, s, e int) string {
	n := len(content)
	if s < 0 {
		s = 0
	}
	if s > n {
		s = n
	}
	if e < s {
		e = s
	}
	if e > n {
		e = n
	}
	if s == e {
		return ""
	}
	for s < n && !utf8.RuneStart(content[s]) {
		s++
	}
	if s > e {
		e = s
	}
	for e > s && e < n && !utf8.RuneStart(content[e]) {
		e--
	}
	return content[s:e]
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

		if in.Action == "" {
			return tool.Result[AssertOutput]{}, fmt.Errorf("memory_assert: action must be set (add|update|delete)")
		}

		now := time.Now().UnixMilli()
		var memoryID string
		var status string

		switch in.Action {
		case "add":
			if in.Content == "" {
				return tool.Result[AssertOutput]{}, fmt.Errorf("memory_assert: content required for action=add")
			}
			kind := in.Kind
			if kind == "" {
				kind = "fact"
			}
			memoryID = ulid.Make().String()
			m := store.Memory{
				ID:        memoryID,
				TenantID:  scope.Tenant,
				Kind:      kind,
				Content:   in.Content,
				Context:   in.Context,
				Status:    "active",
				CreatedAt: now,
				UpdatedAt: now,
			}
			if err := svc.Store.Memories().Insert(ctx, scope, m); err != nil {
				return tool.Result[AssertOutput]{}, fmt.Errorf("memory_assert: insert: %w", err)
			}
			status = "active"

		case "update":
			if in.MemoryID == "" {
				return tool.Result[AssertOutput]{}, fmt.Errorf("memory_assert: memory_id required for action=update")
			}
			memoryID = in.MemoryID
			existing, err := svc.Store.Memories().Get(ctx, scope, memoryID)
			if err != nil {
				return tool.Result[AssertOutput]{}, fmt.Errorf("memory_assert: get memory: %w", err)
			}
			if in.Content != "" {
				existing.Content = in.Content
			}
			if in.Context != "" {
				existing.Context = in.Context
			}
			if in.Kind != "" {
				existing.Kind = in.Kind
			}
			existing.UpdatedAt = now
			if err := svc.Store.Memories().Update(ctx, scope, *existing); err != nil {
				return tool.Result[AssertOutput]{}, fmt.Errorf("memory_assert: update: %w", err)
			}
			status = existing.Status

		case "delete":
			if in.MemoryID == "" {
				return tool.Result[AssertOutput]{}, fmt.Errorf("memory_assert: memory_id required for action=delete")
			}
			memoryID = in.MemoryID
			if err := svc.Store.Memories().SetStatus(ctx, scope, memoryID, "deleted", now); err != nil {
				return tool.Result[AssertOutput]{}, fmt.Errorf("memory_assert: set status: %w", err)
			}
			status = "deleted"

		default:
			return tool.Result[AssertOutput]{}, fmt.Errorf("memory_assert: unknown action %q (want add|update|delete)", in.Action)
		}

		out := AssertOutput{MemoryID: memoryID, Action: in.Action, Status: status}
		return tool.Result[AssertOutput]{
			Text:       fmt.Sprintf("Assert %s: memory_id=%s status=%s", in.Action, memoryID, status),
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
