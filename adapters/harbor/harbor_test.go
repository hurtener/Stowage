package harbor_test

// Package harbor_test provides unit tests for the Harbor–Stowage adapter.
//
// AC-3: ToolCatalog registration — verifies Tools() returns all 7 descriptors,
//       each with the expected name and a non-nil Invoke function.
// AC-4: WireOutcomes fake bus — verifies that a synthetic task.completed event
//       triggers a Feedback "use" call on the Stowage client.
//
// The tests use fake/stub implementations of both the Harbor EventBus and the
// Stowage Client interfaces so that no real Harbor or Stowage infrastructure is
// required at test time.

import (
	"context"
	"errors"
	"encoding/json"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	harborevents "github.com/hurtener/Harbor/sdk/events"
	harboridentity "github.com/hurtener/Harbor/sdk/identity"

	stowage "github.com/hurtener/stowage/sdk/stowage"
	. "github.com/hurtener/stowage/adapters/harbor"
)

// ── Fake Stowage Client ──────────────────────────────────────────────────────

// fakeClient records the calls made to it so tests can assert on them.
type fakeClient struct {
	mu        sync.Mutex
	feedbacks []stowage.FeedbackRequest
	ingests   []stowage.IngestRequest
}

func (f *fakeClient) Ingest(ctx context.Context, req stowage.IngestRequest) (stowage.IngestResponse, error) {
	f.mu.Lock()
	f.ingests = append(f.ingests, req)
	f.mu.Unlock()
	ids := make([]string, len(req.Records))
	for i := range req.Records {
		ids[i] = "fake-id"
	}
	return stowage.IngestResponse{IDs: ids, Enqueued: true}, nil
}

func (f *fakeClient) Retrieve(_ context.Context, _ stowage.RetrieveRequest) (stowage.RetrieveResponse, error) {
	return stowage.RetrieveResponse{API: "v1", ResponseID: "fake-resp"}, nil
}

func (f *fakeClient) Drilldown(_ context.Context, _ stowage.DrilldownRequest) (stowage.DrilldownResponse, error) {
	return stowage.DrilldownResponse{}, errors.New("fake: not found")
}

func (f *fakeClient) Feedback(ctx context.Context, req stowage.FeedbackRequest) (stowage.FeedbackResponse, error) {
	f.mu.Lock()
	f.feedbacks = append(f.feedbacks, req)
	f.mu.Unlock()
	return stowage.FeedbackResponse{Applied: 1, Signal: req.Signal}, nil
}

func (f *fakeClient) ResolveCitations(_ context.Context, req stowage.ResolveCitationsRequest) (stowage.ResolveCitationsResponse, error) {
	items := make([]stowage.ResolveItem, len(req.Citations))
	for i, c := range req.Citations {
		items[i] = stowage.ResolveItem{Citation: c, Found: false}
	}
	return stowage.ResolveCitationsResponse{Items: items}, nil
}

func (f *fakeClient) Topics(_ context.Context) (stowage.TopicsResponse, error) {
	return stowage.TopicsResponse{Topics: []stowage.TopicView{}}, nil
}

func (f *fakeClient) Playbook(_ context.Context, _ stowage.PlaybookRequest) (stowage.PlaybookResponse, error) {
	return stowage.PlaybookResponse{Entries: []any{}, Stub: true}, nil
}

// ── Fake Harbor EventBus ─────────────────────────────────────────────────────

// fakeSub is a simple in-memory subscription.
type fakeSub struct {
	ch     chan harborevents.Event
	cancel func()
}

func (s *fakeSub) Events() <-chan harborevents.Event { return s.ch }
func (s *fakeSub) Cancel()                           { s.cancel() }

// fakeBus is a minimal in-memory event bus for testing WireOutcomes.
type fakeBus struct {
	mu   sync.Mutex
	subs []*fakeSub
}

func (b *fakeBus) Publish(ctx context.Context, ev harborevents.Event) error {
	b.mu.Lock()
	subs := make([]*fakeSub, len(b.subs))
	copy(subs, b.subs)
	b.mu.Unlock()
	for _, s := range subs {
		select {
		case s.ch <- ev:
		default: // drop if subscriber is slow
		}
	}
	return nil
}

func (b *fakeBus) Subscribe(_ context.Context, _ harborevents.Filter) (harborevents.Subscription, error) {
	ch := make(chan harborevents.Event, 32)
	sub := &fakeSub{ch: ch}
	b.mu.Lock()
	b.subs = append(b.subs, sub)
	idx := len(b.subs) - 1
	b.mu.Unlock()
	sub.cancel = func() {
		close(ch)
		b.mu.Lock()
		b.subs[idx] = nil // tombstone
		b.mu.Unlock()
	}
	return sub, nil
}

func (b *fakeBus) Close(_ context.Context) error {
	b.mu.Lock()
	for _, s := range b.subs {
		if s != nil {
			close(s.ch)
		}
	}
	b.subs = nil
	b.mu.Unlock()
	return nil
}

// ── AC-3: ToolCatalog registration ──────────────────────────────────────────

// TestTools_Registration verifies that Tools() returns all 7 expected
// descriptors with non-nil Invoke functions (AC-3: tool catalog registration).
func TestTools_Registration(t *testing.T) {
	client := &fakeClient{}
	descs := Tools(client)

	expectedNames := []string{
		"stowage_ingest",
		"stowage_retrieve",
		"stowage_feedback",
		"stowage_drilldown",
		"stowage_resolve",
		"stowage_topics",
		"stowage_playbook",
	}

	if len(descs) != len(expectedNames) {
		t.Fatalf("Tools: want %d descriptors, got %d", len(expectedNames), len(descs))
	}

	nameSet := make(map[string]bool, len(expectedNames))
	for _, name := range expectedNames {
		nameSet[name] = true
	}
	for _, d := range descs {
		if !nameSet[d.Tool.Name] {
			t.Errorf("Tools: unexpected tool name %q", d.Tool.Name)
		}
		if d.Invoke == nil {
			t.Errorf("Tools: %q has nil Invoke", d.Tool.Name)
		}
		delete(nameSet, d.Tool.Name)
	}
	for name := range nameSet {
		t.Errorf("Tools: missing expected tool %q", name)
	}
}

// TestTools_IngestRoundTrip verifies that the stowage_ingest tool descriptor
// can be invoked directly and calls through to the Stowage client.
// The test stamps a Harbor identity Quadruple on the context (AC-3 identity lift).
func TestTools_IngestRoundTrip(t *testing.T) {
	client := &fakeClient{}
	descs := Tools(client)

	// Find the stowage_ingest descriptor.
	var ingestDesc *harborevents.Event // reuse type placeholder
	_ = ingestDesc
	var ingestInvoke func(ctx context.Context, args json.RawMessage) (interface{ }, error)
	for _, d := range descs {
		if d.Tool.Name == "stowage_ingest" {
			invoke := d.Invoke // copy for closure
			ingestInvoke = func(ctx context.Context, args json.RawMessage) (interface{ }, error) {
				return invoke(ctx, args)
			}
			break
		}
	}
	if ingestInvoke == nil {
		t.Fatal("stowage_ingest not found in Tools() result")
	}

	// Stamp a Harbor identity quadruple on the context (AC-3: identity lift).
	id := harboridentity.Identity{TenantID: "test-tenant", UserID: "test-user", SessionID: "test-sess"}
	ctx, err := harboridentity.WithRun(context.Background(), id, "test-run-1")
	if err != nil {
		t.Fatalf("harboridentity.WithRun: %v", err)
	}

	// Invoke the tool with a valid ingest payload.
	args := json.RawMessage(`{"records": [{"role": "user", "content": "hello"}]}`)
	result, err := ingestInvoke(ctx, args)
	if err != nil {
		t.Fatalf("stowage_ingest Invoke: %v", err)
	}
	if result == nil {
		t.Error("stowage_ingest Invoke: want non-nil result")
	}

	// Verify the fake client received the ingest call.
	client.mu.Lock()
	nIngests := len(client.ingests)
	client.mu.Unlock()
	if nIngests == 0 {
		t.Error("stowage_ingest Invoke: fakeClient.Ingest was not called")
	}
}

// ── AC-4: WireOutcomes fake bus ──────────────────────────────────────────────

// TestWireOutcomes_TaskCompleted verifies that a task.completed event on the
// fake bus triggers a "use" feedback signal on the Stowage client (AC-4).
func TestWireOutcomes_TaskCompleted(t *testing.T) {
	client := &fakeClient{}
	bus := &fakeBus{}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	ctx := context.Background()
	wirer, stop := WireOutcomes(ctx, bus, client, log)
	defer stop()

	const runID = "test-run-completed"
	const respID = "test-resp-completed"

	wirer.RegisterResponseID(runID, respID)

	// Publish a task.completed event with the run ID.
	id := harboridentity.Identity{TenantID: "t", UserID: "u", SessionID: "s"}
	ev := harborevents.Event{
		Type:       "task.completed",
		Identity:   harboridentity.Quadruple{Identity: id, RunID: runID},
		OccurredAt: time.Now(),
	}
	if err := bus.Publish(ctx, ev); err != nil {
		t.Fatalf("bus.Publish: %v", err)
	}

	// Give the async goroutine time to process the event.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		client.mu.Lock()
		n := len(client.feedbacks)
		client.mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	client.mu.Lock()
	feedbacks := make([]stowage.FeedbackRequest, len(client.feedbacks))
	copy(feedbacks, client.feedbacks)
	client.mu.Unlock()

	if len(feedbacks) == 0 {
		t.Fatal("WireOutcomes: expected Feedback call on task.completed, got none")
	}
	fb := feedbacks[0]
	if fb.ResponseID != respID {
		t.Errorf("WireOutcomes: feedback ResponseID want %q, got %q", respID, fb.ResponseID)
	}
	if fb.Signal != "use" {
		t.Errorf("WireOutcomes: feedback Signal want %q, got %q", "use", fb.Signal)
	}
}

// TestWireOutcomes_TaskFailed verifies that a task.failed event triggers a
// "fail" feedback signal (AC-4 complement).
func TestWireOutcomes_TaskFailed(t *testing.T) {
	client := &fakeClient{}
	bus := &fakeBus{}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	ctx := context.Background()
	wirer, stop := WireOutcomes(ctx, bus, client, log)
	defer stop()

	const runID = "test-run-failed"
	const respID = "test-resp-failed"

	wirer.RegisterResponseID(runID, respID)

	id := harboridentity.Identity{TenantID: "t", UserID: "u", SessionID: "s"}
	ev := harborevents.Event{
		Type:       "task.failed",
		Identity:   harboridentity.Quadruple{Identity: id, RunID: runID},
		OccurredAt: time.Now(),
	}
	if err := bus.Publish(ctx, ev); err != nil {
		t.Fatalf("bus.Publish: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		client.mu.Lock()
		n := len(client.feedbacks)
		client.mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	client.mu.Lock()
	feedbacks := make([]stowage.FeedbackRequest, len(client.feedbacks))
	copy(feedbacks, client.feedbacks)
	client.mu.Unlock()

	if len(feedbacks) == 0 {
		t.Fatal("WireOutcomes: expected Feedback call on task.failed, got none")
	}
	fb := feedbacks[0]
	if fb.Signal != "fail" {
		t.Errorf("WireOutcomes: feedback Signal want %q, got %q", "fail", fb.Signal)
	}
}

// TestWireOutcomes_NoResponseID verifies that events without a registered
// response_id do not trigger Feedback calls.
func TestWireOutcomes_NoResponseID(t *testing.T) {
	client := &fakeClient{}
	bus := &fakeBus{}

	ctx := context.Background()
	_, stop := WireOutcomes(ctx, bus, client, nil)
	defer stop()

	id := harboridentity.Identity{TenantID: "t", UserID: "u", SessionID: "s"}
	ev := harborevents.Event{
		Type:       "task.completed",
		Identity:   harboridentity.Quadruple{Identity: id, RunID: "run-with-no-registered-resp"},
		OccurredAt: time.Now(),
	}
	if err := bus.Publish(ctx, ev); err != nil {
		t.Fatalf("bus.Publish: %v", err)
	}

	// Small sleep to let the goroutine process (should be a no-op).
	time.Sleep(50 * time.Millisecond)

	client.mu.Lock()
	n := len(client.feedbacks)
	client.mu.Unlock()

	if n != 0 {
		t.Errorf("WireOutcomes: expected 0 Feedback calls for unregistered run, got %d", n)
	}
}
