// Package harbor provides the Stowage–Harbor integration adapter (D-063).
//
// Harbor's dependency tree (67+ dependencies) must never enter the Stowage
// core module. This separate module (github.com/hurtener/stowage/adapters/harbor)
// keeps that boundary hard: the adapter imports both Harbor and Stowage, but
// Stowage core imports neither Harbor nor this adapter.
//
// # Zero-config Harbor wiring (D-032 / D-062)
//
// Harbor has no per-turn middleware hooks. Zero-config memory wiring uses two
// primitives instead:
//
//  1. Tools() — registers the seven Stowage memory operations as in-process
//     Harbor tools on the ToolCatalog via inproc.RegisterFunc. Consumed via
//     assemble.Options.PreRegisterTools in one line:
//
//     opts.PreRegisterTools = append(opts.PreRegisterTools,
//     harbor.Tools(sdkClient)...)
//
//  2. WireOutcomes() — subscribes to task.completed / task.failed on the
//     Harbor event bus and translates each terminal event into a Stowage
//     feedback signal. This captures the outcome (use/fail) for every
//     retrieve response_id the task produced, enabling the quality loop.
//
// # Identity mapping
//
// Harbor identity (TenantID/UserID/SessionID) flows automatically via the
// request context. Inside each tool handler, identity.From(ctx) yields the
// Harbor triple, which becomes the Stowage API key's tenant scope.
//
// # Release process
//
// 1. Bump the Stowage version pin in go.mod (remove the replace directive).
// 2. Tag the adapter at the same semver as the Stowage release.
// 3. Harbor callers pin this adapter version.
//
// During development the replace directive in go.mod resolves Stowage locally.
package harbor

import (
	"context"
	"errors"
	"fmt"
	harborid "github.com/hurtener/Harbor/sdk/identity"
	"log/slog"
	"sync"

	harborevents "github.com/hurtener/Harbor/sdk/events"
	harbortools "github.com/hurtener/Harbor/sdk/tools"
	harborinproc "github.com/hurtener/Harbor/sdk/tools/inproc"

	stowage "github.com/hurtener/stowage/sdk/stowage"
)

// task terminal event types (string constants from internal/tasks — not
// exported by the Harbor SDK, but stable since Phase 05).
const (
	eventTypeTaskCompleted harborevents.EventType = "task.completed"
	eventTypeTaskFailed    harborevents.EventType = "task.failed"
)

// Tools registers the seven Stowage memory operations as in-process Harbor
// tools on the catalog. The returned []tools.ToolDescriptor can be placed
// directly into assemble.Options.PreRegisterTools:
//
//	opts.PreRegisterTools = append(opts.PreRegisterTools, harbor.Tools(sdkClient)...)
//
// Identity is lifted from ctx on every invocation: Harbor's identity triple
// (TenantID → tenant scope, UserID → user, SessionID → session) sets the
// Stowage scope. The Stowage client is shared across all tool invocations and
// MUST be safe for concurrent use (both NewHTTP and NewEmbedded are).
//
// The tools registered are:
//
//	stowage_ingest          — append conversation records
//	stowage_retrieve        — four-lane fusion retrieval
//	stowage_feedback        — apply use/save/fail/noise signal
//	stowage_drilldown       — drill to verbatim provenance
//	stowage_resolve         — resolve citation handles
//	stowage_topics          — list effective topics
//	stowage_playbook        — stub in Phase 17; full assembly later
func Tools(client stowage.Client) []harbortools.ToolDescriptor {
	cat := harbortools.NewCatalog()

	mustRegister(cat, "stowage_ingest", ingestFn(client),
		harbortools.WithDescription("Append conversation records to Stowage memory (fire-and-forget; ACK is immediate)."),
		harbortools.WithSideEffect(harbortools.SideEffectStateful),
	)
	mustRegister(cat, "stowage_retrieve", retrieveFn(client),
		harbortools.WithDescription("Retrieve memories relevant to a query using four-lane fusion (lexical+vector+structured+queries)."),
		harbortools.WithSideEffect(harbortools.SideEffectRead),
	)
	mustRegister(cat, "stowage_feedback", feedbackFn(client),
		harbortools.WithDescription("Apply a quality signal (use|save|fail|noise|wrong_citation) to a retrieval response or memory."),
		harbortools.WithSideEffect(harbortools.SideEffectStateful),
	)
	mustRegister(cat, "stowage_drilldown", drilldownFn(client),
		harbortools.WithDescription("Drill down from a memory or citation to its verbatim source spans (P1 provenance)."),
		harbortools.WithSideEffect(harbortools.SideEffectRead),
	)
	mustRegister(cat, "stowage_resolve", resolveFn(client),
		harbortools.WithDescription("Resolve citation handles (injection ULIDs) to their memory summaries and provenance."),
		harbortools.WithSideEffect(harbortools.SideEffectRead),
	)
	mustRegister(cat, "stowage_topics", topicsFn(client),
		harbortools.WithDescription("List the effective memory topics for this agent's scope."),
		harbortools.WithSideEffect(harbortools.SideEffectRead),
	)
	mustRegister(cat, "stowage_playbook", playbookFn(client),
		harbortools.WithDescription("Return the memory playbook for the current session (stub in Phase 17)."),
		harbortools.WithSideEffect(harbortools.SideEffectRead),
	)

	// Collect registered descriptors.
	var descs []harbortools.ToolDescriptor
	for _, name := range harbortools.VisibleNames(cat, harbortools.CatalogFilter{}) {
		d, ok := cat.Resolve(name)
		if ok {
			descs = append(descs, d)
		}
	}
	return descs
}

// mustRegister registers a function tool and panics on schema error. Schema
// build failures indicate a programming error in the tool's input/output types
// and should be caught at startup, not silently ignored.
func mustRegister[I any, O any](
	cat harbortools.ToolCatalog,
	name string,
	fn func(ctx context.Context, in I) (O, error),
	opts ...harbortools.DescriptorOption,
) {
	if err := harborinproc.RegisterFunc(cat, name, fn, opts...); err != nil {
		panic(fmt.Sprintf("harbor adapter: register %q: %v", name, err))
	}
}

// ---- Tool input/output types ------------------------------------------------
// These types define the JSON schema that Harbor derives via reflection and
// exposes to the planner.

type ingestIn struct {
	Records []stowage.RecordInput `json:"records" jsonschema:"description=Records to append"`
}
type ingestOut struct {
	IDs      []string `json:"ids"`
	Enqueued bool     `json:"enqueued"`
}

type retrieveIn struct {
	Query     string `json:"query"  jsonschema:"description=Free-text memory query"`
	Limit     int    `json:"limit"  jsonschema:"description=Max results (default 10)"`
	Profile   string `json:"profile,omitempty" jsonschema:"description=Retrieval preset: precise|balanced|broad"`
	SessionID string `json:"session_id,omitempty" jsonschema:"description=Override the session scope (defaults to the Harbor caller's session)"`
}
type retrieveOut struct {
	ResponseID string               `json:"response_id"`
	Items      []stowage.MemoryItem `json:"items"`
	Degraded   bool                 `json:"degraded"`
}

type feedbackIn struct {
	ResponseID string `json:"response_id,omitempty"`
	MemoryID   string `json:"memory_id,omitempty"`
	Citation   string `json:"citation,omitempty"`
	Signal     string `json:"signal" jsonschema:"description=Signal: use|save|fail|noise|wrong_citation"`
}
type feedbackOut struct {
	Applied int    `json:"applied"`
	Signal  string `json:"signal"`
}

type drilldownIn struct {
	MemoryID string `json:"memory_id,omitempty" jsonschema:"description=Memory ID to drill into"`
	Citation string `json:"citation,omitempty"  jsonschema:"description=Citation handle to resolve"`
}
type drilldownOut struct {
	MemoryID string                  `json:"memory_id"`
	Spans    []stowage.DrilldownSpan `json:"spans"`
}

type resolveIn struct {
	Citations []string `json:"citations" jsonschema:"description=Citation handles (injection ULIDs)"`
}
type resolveOut struct {
	Items []stowage.ResolveItem `json:"items"`
}

type topicsIn struct{}
type topicsOut struct {
	Topics []stowage.TopicView `json:"topics"`
}

type playbookIn struct {
	SessionID string `json:"session_id,omitempty"`
	Limit     int    `json:"limit,omitempty"`
}
type playbookOut struct {
	Entries []any `json:"entries"`
	Stub    bool  `json:"stub"`
}

// ---- Tool handler factories -------------------------------------------------

// liftIdentity merges the Harbor request identity into per-call user/session
// fields. Input-provided values win; otherwise the Harbor caller's identity
// flows through so a multi-user Harbor app never collapses into one scope.
// The Harbor runtime stamps the QUADRUPLE context key (WithRun); With stamps
// a separate Identity key — they don't satisfy each other, so read the
// quadruple first. Tenant comes from the SDK client's API key server-side.
func liftIdentity(ctx context.Context, user, session string) (string, string) {
	id, ok := func() (harborid.Identity, bool) {
		if q, qok := harborid.QuadrupleFrom(ctx); qok {
			return q.Identity, true
		}
		return harborid.From(ctx)
	}()
	if ok {
		if user == "" {
			user = id.UserID
		}
		if session == "" {
			session = id.SessionID
		}
	}
	return user, session
}

func ingestFn(client stowage.Client) func(context.Context, ingestIn) (ingestOut, error) {
	return func(ctx context.Context, in ingestIn) (ingestOut, error) {
		for i := range in.Records {
			in.Records[i].UserID, in.Records[i].SessionID =
				liftIdentity(ctx, in.Records[i].UserID, in.Records[i].SessionID)
		}
		resp, err := client.Ingest(ctx, stowage.IngestRequest{Records: in.Records})
		if err != nil {
			return ingestOut{}, err
		}
		return ingestOut{IDs: resp.IDs, Enqueued: resp.Enqueued}, nil
	}
}

func retrieveFn(client stowage.Client) func(context.Context, retrieveIn) (retrieveOut, error) {
	return func(ctx context.Context, in retrieveIn) (retrieveOut, error) {
		limit := in.Limit
		if limit == 0 {
			limit = 10
		}
		// Session lifts from Harbor identity (cooldown correctness); user-level
		// READ scoping is server-side (key tenant + grants), never client-asserted.
		_, session := liftIdentity(ctx, "", in.SessionID)
		resp, err := client.Retrieve(ctx, stowage.RetrieveRequest{
			Query: in.Query, Limit: limit, Profile: in.Profile,
			SessionID: session,
		})
		if err != nil {
			return retrieveOut{}, err
		}
		return retrieveOut{
			ResponseID: resp.ResponseID, Items: resp.Items, Degraded: resp.Degraded,
		}, nil
	}
}

func feedbackFn(client stowage.Client) func(context.Context, feedbackIn) (feedbackOut, error) {
	return func(ctx context.Context, in feedbackIn) (feedbackOut, error) {
		resp, err := client.Feedback(ctx, stowage.FeedbackRequest{
			ResponseID: in.ResponseID, MemoryID: in.MemoryID,
			Citation: in.Citation, Signal: in.Signal,
		})
		if err != nil {
			return feedbackOut{}, err
		}
		return feedbackOut{Applied: resp.Applied, Signal: resp.Signal}, nil
	}
}

func drilldownFn(client stowage.Client) func(context.Context, drilldownIn) (drilldownOut, error) {
	return func(ctx context.Context, in drilldownIn) (drilldownOut, error) {
		resp, err := client.Drilldown(ctx, stowage.DrilldownRequest{
			MemoryID: in.MemoryID, Citation: in.Citation,
		})
		if err != nil {
			return drilldownOut{}, err
		}
		return drilldownOut{MemoryID: resp.MemoryID, Spans: resp.Spans}, nil
	}
}

func resolveFn(client stowage.Client) func(context.Context, resolveIn) (resolveOut, error) {
	return func(ctx context.Context, in resolveIn) (resolveOut, error) {
		resp, err := client.ResolveCitations(ctx, stowage.ResolveCitationsRequest{
			Citations: in.Citations,
		})
		if err != nil {
			return resolveOut{}, err
		}
		return resolveOut{Items: resp.Items}, nil
	}
}

func topicsFn(client stowage.Client) func(context.Context, topicsIn) (topicsOut, error) {
	return func(ctx context.Context, _ topicsIn) (topicsOut, error) {
		resp, err := client.Topics(ctx)
		if err != nil {
			return topicsOut{}, err
		}
		return topicsOut{Topics: resp.Topics}, nil
	}
}

func playbookFn(client stowage.Client) func(context.Context, playbookIn) (playbookOut, error) {
	return func(ctx context.Context, in playbookIn) (playbookOut, error) {
		resp, err := client.Playbook(ctx, stowage.PlaybookRequest{
			SessionID: in.SessionID, Limit: in.Limit,
		})
		if err != nil {
			return playbookOut{}, err
		}
		return playbookOut{Entries: resp.Entries, Stub: resp.Stub}, nil
	}
}

// ---- WireOutcomes -----------------------------------------------------------

// OutcomeWirer subscribes to the Harbor event bus and translates terminal task
// events (task.completed / task.failed) into Stowage feedback signals. Wire it
// after assembling the Harbor stack:
//
//	wirer, stop := harbor.WireOutcomes(ctx, stack.Bus, sdkClient, slog.Default())
//	defer stop()
//
// The wirer correlates retrieve response_ids to tasks via an in-memory map
// (response_id → runID, populated by the stowage_retrieve tool's return value).
// On task completion the map is consulted: if the task produced retrieval
// responses, a "use" (completed) or "fail" (failed) signal is sent.
//
// The stop function cancels the subscription and waits for the goroutine to
// drain cleanly. Call it before closing the Harbor stack.
type OutcomeWirer struct {
	mu          sync.Mutex
	responseIDs map[string]string // runID → responseID
	client      stowage.Client
	log         *slog.Logger
}

// WireOutcomes subscribes to task.completed and task.failed on bus and sends
// feedback signals to client for any retrieve response_ids the task produced.
// Returns the wirer (which can also be used to register response_ids manually)
// and a stop function.
func WireOutcomes(ctx context.Context, bus harborevents.EventBus, client stowage.Client, log *slog.Logger) (*OutcomeWirer, func()) {
	w := &OutcomeWirer{
		responseIDs: make(map[string]string),
		client:      client,
		log:         log,
	}

	sub, err := bus.Subscribe(ctx, harborevents.Filter{
		Admin: true, // cross-session admin scope to observe all tasks
		Types: []harborevents.EventType{
			eventTypeTaskCompleted,
			eventTypeTaskFailed,
		},
	})
	if err != nil {
		// Bus subscription failure is non-fatal; log and return a noop stop.
		if log != nil {
			log.Warn("stowage harbor: WireOutcomes subscribe failed", "err", err)
		}
		return w, func() {}
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range sub.Events() {
			w.handleEvent(ctx, ev)
		}
	}()

	stop := func() {
		sub.Cancel()
		<-done
	}
	return w, stop
}

// RegisterResponseID records a retrieve response_id produced during a run.
// Call this from a tool wrapper or event subscriber to correlate the response
// with its run for feedback wiring. The typical pattern is to call it from
// within the stowage_retrieve tool handler.
//
// runID is the Harbor RunID (from identity.Quadruple.RunID).
func (w *OutcomeWirer) RegisterResponseID(runID, responseID string) {
	if runID == "" || responseID == "" {
		return
	}
	w.mu.Lock()
	w.responseIDs[runID] = responseID
	w.mu.Unlock()
}

func (w *OutcomeWirer) handleEvent(ctx context.Context, ev harborevents.Event) {
	runID := ev.Identity.RunID
	if runID == "" {
		return
	}

	w.mu.Lock()
	responseID, ok := w.responseIDs[runID]
	if ok {
		delete(w.responseIDs, runID)
	}
	w.mu.Unlock()

	if !ok {
		return // no retrieve responses in this run
	}

	signal := "fail"
	if ev.Type == eventTypeTaskCompleted {
		signal = "use"
	}

	_, err := w.client.Feedback(ctx, stowage.FeedbackRequest{
		ResponseID: responseID,
		Signal:     signal,
	})
	if err != nil && !errors.Is(err, context.Canceled) {
		if w.log != nil {
			w.log.Warn("stowage harbor: feedback on task terminal", "run_id", runID, "signal", signal, "err", err)
		}
	}
}
