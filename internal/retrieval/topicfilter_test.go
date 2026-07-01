package retrieval_test

// topicfilter_test.go — unit coverage for the ae6 own-scope topic filter
// (D-139/D-144): filterByTopicOwnScope (fail-OPEN) vs grants' filterByTopic
// (fail-CLOSED), plus the no-underfill regression that pins the pre-trim
// placement (D-144) against the naive "filter the scoringK-trimmed pool"
// approach the plan explicitly rejects.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/retrieval"
	"github.com/hurtener/stowage/internal/store"
	"github.com/hurtener/stowage/internal/vindex"
)

// commitMemoryFull inserts an active memory carrying full junction control
// (entities/keywords/anticipated-queries/topics) via Commit, for lane- and
// topic-precise fixtures.
func commitMemoryFull(t *testing.T, st store.Store, scope identity.Scope, content, kind string, entities, keywords, queries, topics []string) string {
	t.Helper()
	id := newID()
	now := time.Now().UnixMilli()
	cs := store.CommitSet{
		Action: store.ActionAdd,
		Memory: store.Memory{
			ID: id, Kind: kind, Content: content, Status: "active",
			Importance: 3, Confidence: 0.8, TrustSource: "llm_extracted", Stability: 1.0,
			PrivacyZone: "public", CreatedAt: now, UpdatedAt: now,
		},
		Entities: entities,
		Keywords: keywords,
		Queries:  queries,
		Topics:   topics,
		Scope:    scope,
	}
	if err := st.Memories().Commit(context.Background(), scope, cs); err != nil {
		t.Fatalf("commitMemoryFull: %v", err)
	}
	return id
}

// memoriesTopicsFailStore wraps a real store.MemoryStore but fails every
// MemoriesTopics call, to exercise the D-139 fail-open/fail-closed divergence
// deterministically (mirrors hubSignalsFailStore's pattern in retrieval_test.go).
type memoriesTopicsFailStore struct {
	store.MemoryStore
}

func (m memoriesTopicsFailStore) MemoriesTopics(context.Context, identity.Scope, []string) (map[string][]string, error) {
	return nil, errors.New("synthetic MemoriesTopics failure")
}

// --- include / exclude / both / empty / no-match -----------------------------

func TestTopicFilterOwnScope_IncludeOnly(t *testing.T) {
	t.Parallel()
	st := openStore(t)
	gw := openMockGateway(t, 4)
	vi := vindex.New(st.Vectors(), 4, "test")
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	r := retrieval.New(st.Memories(), st.Records(), vi, gw, log)

	scope := identity.Scope{Tenant: "t-topicfilter-include"}
	authID := commitMemoryFull(t, st, scope, "auth note", "fact", nil, nil, nil, []string{"auth"})
	deployID := commitMemoryFull(t, st, scope, "deploy note", "fact", nil, nil, nil, []string{"deploy"})
	untaggedID := commitMemoryFull(t, st, scope, "untagged note", "fact", nil, nil, nil, nil)

	kept, degraded := r.ExportFilterByTopicOwnScope(context.Background(), scope,
		[]string{authID, deployID, untaggedID}, []string{"auth"}, nil)
	if degraded {
		t.Fatal("unexpected degraded=true")
	}
	assertIDSet(t, "include=[auth]", kept, []string{authID})
}

func TestTopicFilterOwnScope_ExcludeOnly(t *testing.T) {
	t.Parallel()
	st := openStore(t)
	gw := openMockGateway(t, 4)
	vi := vindex.New(st.Vectors(), 4, "test")
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	r := retrieval.New(st.Memories(), st.Records(), vi, gw, log)

	scope := identity.Scope{Tenant: "t-topicfilter-exclude"}
	authID := commitMemoryFull(t, st, scope, "auth note", "fact", nil, nil, nil, []string{"auth"})
	deployID := commitMemoryFull(t, st, scope, "deploy note", "fact", nil, nil, nil, []string{"deploy"})
	untaggedID := commitMemoryFull(t, st, scope, "untagged note", "fact", nil, nil, nil, nil)

	kept, degraded := r.ExportFilterByTopicOwnScope(context.Background(), scope,
		[]string{authID, deployID, untaggedID}, nil, []string{"deploy"})
	if degraded {
		t.Fatal("unexpected degraded=true")
	}
	assertIDSet(t, "exclude=[deploy]", kept, []string{authID, untaggedID})
}

func TestTopicFilterOwnScope_IncludeAndExclude(t *testing.T) {
	t.Parallel()
	st := openStore(t)
	gw := openMockGateway(t, 4)
	vi := vindex.New(st.Vectors(), 4, "test")
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	r := retrieval.New(st.Memories(), st.Records(), vi, gw, log)

	scope := identity.Scope{Tenant: "t-topicfilter-both"}
	// Tagged both "auth" and "deploy" — must be EXCLUDED even though it matches include.
	bothID := commitMemoryFull(t, st, scope, "auth+deploy note", "fact", nil, nil, nil, []string{"auth", "deploy"})
	authOnlyID := commitMemoryFull(t, st, scope, "auth only note", "fact", nil, nil, nil, []string{"auth"})
	deployOnlyID := commitMemoryFull(t, st, scope, "deploy only note", "fact", nil, nil, nil, []string{"deploy"})

	kept, degraded := r.ExportFilterByTopicOwnScope(context.Background(), scope,
		[]string{bothID, authOnlyID, deployOnlyID}, []string{"auth"}, []string{"deploy"})
	if degraded {
		t.Fatal("unexpected degraded=true")
	}
	assertIDSet(t, "include=[auth] exclude=[deploy]", kept, []string{authOnlyID})
}

func TestTopicFilterOwnScope_EmptyPassThrough(t *testing.T) {
	t.Parallel()
	st := openStore(t)
	gw := openMockGateway(t, 4)
	vi := vindex.New(st.Vectors(), 4, "test")
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	r := retrieval.New(st.Memories(), st.Records(), vi, gw, log)

	scope := identity.Scope{Tenant: "t-topicfilter-passthrough"}
	id1 := commitMemoryFull(t, st, scope, "note one", "fact", nil, nil, nil, []string{"auth"})
	id2 := commitMemoryFull(t, st, scope, "note two", "fact", nil, nil, nil, nil)

	kept, degraded := r.ExportFilterByTopicOwnScope(context.Background(), scope, []string{id1, id2}, nil, nil)
	if degraded {
		t.Fatal("unexpected degraded=true")
	}
	assertIDSet(t, "empty include/exclude", kept, []string{id1, id2})
}

func TestTopicFilterOwnScope_NoMatchYieldsEmpty(t *testing.T) {
	t.Parallel()
	st := openStore(t)
	gw := openMockGateway(t, 4)
	vi := vindex.New(st.Vectors(), 4, "test")
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	r := retrieval.New(st.Memories(), st.Records(), vi, gw, log)

	scope := identity.Scope{Tenant: "t-topicfilter-nomatch"}
	id1 := commitMemoryFull(t, st, scope, "note one", "fact", nil, nil, nil, []string{"auth"})
	id2 := commitMemoryFull(t, st, scope, "note two", "fact", nil, nil, nil, []string{"deploy"})

	kept, degraded := r.ExportFilterByTopicOwnScope(context.Background(), scope, []string{id1, id2}, []string{"nonexistent"}, nil)
	if degraded {
		t.Fatal("unexpected degraded=true")
	}
	if len(kept) != 0 {
		t.Errorf("include=[nonexistent]: got %v want empty", kept)
	}
}

// --- fail-open (D-139) vs grants' fail-closed contrast ------------------------

// TestTopicFilterOwnScope_FailsOpen proves that a MemoriesTopics error returns
// the caller's own candidate ids UNCHANGED with degraded=true (D-139).
func TestTopicFilterOwnScope_FailsOpen(t *testing.T) {
	t.Parallel()
	st := openStore(t)
	gw := openMockGateway(t, 4)
	vi := vindex.New(st.Vectors(), 4, "test")
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	r := retrieval.New(memoriesTopicsFailStore{st.Memories()}, st.Records(), vi, gw, log)

	scope := identity.Scope{Tenant: "t-topicfilter-failopen"}
	ids := []string{"m1", "m2", "m3"}

	kept, degraded := r.ExportFilterByTopicOwnScope(context.Background(), scope, ids, []string{"auth"}, nil)
	if !degraded {
		t.Fatal("expected degraded=true on a MemoriesTopics error")
	}
	assertIDSet(t, "fail-open", kept, ids)
}

// TestTopicFilterDivergence_SameErrorOppositeOutcome is the D-139 pin: on the
// EXACT SAME injected MemoriesTopics error, grants' filterByTopic fails CLOSED
// (nil — drop the granted scope) while filterByTopicOwnScope fails OPEN (the
// caller's own unfiltered ids, degraded=true). The two are distinct functions
// with intentionally opposite error semantics — this test guards against a
// future "make them consistent" refactor merging them.
func TestTopicFilterDivergence_SameErrorOppositeOutcome(t *testing.T) {
	t.Parallel()
	st := openStore(t)
	gw := openMockGateway(t, 4)
	vi := vindex.New(st.Vectors(), 4, "test")
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	r := retrieval.New(memoriesTopicsFailStore{st.Memories()}, st.Records(), vi, gw, log)

	scope := identity.Scope{Tenant: "t-divergence", User: "alice"}

	// grants' filterByTopic (cross-scope sharing): fails CLOSED.
	mems := []store.Memory{{ID: "m1"}, {ID: "m2"}}
	gotGrants := r.ExportFilterByTopic(context.Background(), scope, mems, "auth")
	if gotGrants != nil {
		t.Errorf("grants' filterByTopic must fail CLOSED (nil) on a MemoriesTopics error, got %v", gotGrants)
	}

	// ae6's filterByTopicOwnScope (own-scope curation): fails OPEN.
	kept, degraded := r.ExportFilterByTopicOwnScope(context.Background(), scope, []string{"m1", "m2"}, []string{"auth"}, nil)
	if !degraded {
		t.Error("filterByTopicOwnScope must fail OPEN (degraded=true) on the SAME error")
	}
	assertIDSet(t, "own-scope fail-open", kept, []string{"m1", "m2"})
}

// TestRetrieve_TopicFilterFailsOpen_DegradedMarker proves the D-139 fail-open
// wiring end to end through Retrieve: a MemoriesTopics error surfaces the
// caller's own UNFILTERED results with DegradedTopicFilter=true, never an
// error and never a dropped result.
func TestRetrieve_TopicFilterFailsOpen_DegradedMarker(t *testing.T) {
	t.Parallel()
	st := openStore(t)
	gw := openMockGateway(t, 4)
	vi := vindex.New(st.Vectors(), 4, "test")
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	r := retrieval.New(memoriesTopicsFailStore{st.Memories()}, st.Records(), vi, gw, log)

	scope := identity.Scope{Tenant: "t-retrieve-failopen-" + newID()}
	memID := insertMemory(t, st, scope, "widget failing open test unique term qvzx", "fact", nil, nil, nil, 0)

	resp, err := r.Retrieve(context.Background(), scope, retrieval.Request{
		Query:         "widget failing open unique qvzx",
		Limit:         5,
		IncludeTopics: []string{"nonexistent-topic"},
	})
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if !resp.DegradedTopicFilter {
		t.Error("expected DegradedTopicFilter=true when MemoriesTopics fails")
	}
	found := false
	for _, it := range resp.Items {
		if it.Memory.ID == memID {
			found = true
		}
	}
	if !found {
		t.Error("fail-open: expected the caller's own unfiltered memory in the result")
	}
}

// --- no-underfill (AC-2, the core risk) ---------------------------------------

// TestTopicFilter_NoUnderfill_PinsPreTrimPlacement is the AC-2 regression: with
// >= limit on-topic memories ranked BELOW the profile's default scoringK (but
// within laneK), Retrieve still returns `limit` items. The test independently
// replays the naive "filter the scoringK-trimmed pool" approach the plan
// explicitly rejected (D-144) — over the SAME lane data — and proves it would
// underfill (0 on-topic survivors in the trimmed top-scoringK), while the real
// (pinned) Retrieve call returns the full on-topic set.
func TestTopicFilter_NoUnderfill_PinsPreTrimPlacement(t *testing.T) {
	t.Parallel()
	st := openStore(t)
	gw := openMockGateway(t, 4)
	vi := vindex.New(st.Vectors(), 4, "test")
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	r := retrieval.New(st.Memories(), st.Records(), vi, gw, log)

	scope := identity.Scope{Tenant: "t-topicfilter-underfill-" + newID()}
	ctx := context.Background()

	// Balanced profile (the default): LaneK=100, ScoringK=20, DefaultLimit=10.
	const offTopicCount = 25 // > ScoringK(20): dominates the fused top-20.
	const onTopicCount = 12  // requested limit; must all survive filtering.
	const query = "gizmoqvzx"

	// Off-topic: hits ALL THREE non-vector lanes (lexical + queries + structured),
	// so its RRF score is far higher than a lexical-only hit — it dominates the
	// fused top-ScoringK regardless of bm25 tie-break details.
	offIDs := make([]string, 0, offTopicCount)
	for i := 0; i < offTopicCount; i++ {
		id := commitMemoryFull(t, st, scope, fmt.Sprintf("%s note off-topic %d", query, i), "fact",
			[]string{query}, []string{query}, []string{query + " note"}, nil)
		offIDs = append(offIDs, id)
	}

	// On-topic: lexical-lane-only hit (weaker RRF), tagged "target". These must
	// rank BELOW the top ScoringK(20) in the raw fused order.
	onIDs := make([]string, 0, onTopicCount)
	for i := 0; i < onTopicCount; i++ {
		id := commitMemoryFull(t, st, scope, fmt.Sprintf("%s config on-topic %d", query, i), "fact",
			nil, nil, nil, []string{"target"})
		onIDs = append(onIDs, id)
	}

	// --- Independently prove the on-topic set ranks below ScoringK(20) --------
	lexHits, err := st.Memories().LexicalSearch(ctx, scope, query, 100, store.Window{}, nil)
	if err != nil {
		t.Fatalf("LexicalSearch: %v", err)
	}
	qHits, err := st.Memories().QuerySearch(ctx, scope, query, 100, store.Window{})
	if err != nil {
		t.Fatalf("QuerySearch: %v", err)
	}
	neighbors, err := st.Memories().FindNeighbors(ctx, scope, store.NeighborQuery{
		Entities: []string{query}, Keywords: []string{query}, Limit: 100,
	})
	if err != nil {
		t.Fatalf("FindNeighbors: %v", err)
	}
	lanes := map[string][]string{}
	for _, h := range lexHits {
		lanes["lexical"] = append(lanes["lexical"], h.MemoryID)
	}
	for _, h := range qHits {
		lanes["queries"] = append(lanes["queries"], h.MemoryID)
	}
	for _, n := range neighbors {
		lanes["structured"] = append(lanes["structured"], n.ID)
	}
	fused := retrieval.ExportRRF(lanes)

	const balancedScoringK = 20
	naive := fused
	if len(naive) > balancedScoringK {
		naive = naive[:balancedScoringK]
	}
	onSet := make(map[string]bool, len(onIDs))
	for _, id := range onIDs {
		onSet[id] = true
	}
	naiveSurvivors := 0
	for _, h := range naive {
		if onSet[h.MemoryID] {
			naiveSurvivors++
		}
	}
	if naiveSurvivors != 0 {
		t.Fatalf("fixture invariant broken: %d on-topic ids survived the naive top-%d trim (want 0 — the fixture must rank on-topic below ScoringK)", naiveSurvivors, balancedScoringK)
	}

	// --- The real (pinned) Retrieve must still fill `limit` -------------------
	resp, err := r.Retrieve(ctx, scope, retrieval.Request{
		Query:         query,
		Limit:         onTopicCount,
		Profile:       "balanced",
		IncludeTopics: []string{"target"},
	})
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if resp.DegradedTopicFilter {
		t.Error("expected DegradedTopicFilter=false on a clean topic-store read")
	}
	if len(resp.Items) != onTopicCount {
		t.Fatalf("no-underfill (AC-2) violated: got %d items, want %d (limit) — the naive scoringK-trimmed-pool approach would have returned 0",
			len(resp.Items), onTopicCount)
	}
	gotIDs := make(map[string]bool, len(resp.Items))
	for _, it := range resp.Items {
		gotIDs[it.Memory.ID] = true
	}
	for _, id := range onIDs {
		if !gotIDs[id] {
			t.Errorf("missing on-topic memory %s from the filled result", id)
		}
	}
	_ = offIDs
}

// assertIDSet fails unless got contains exactly the ids in want (order-agnostic).
func assertIDSet(t *testing.T, label string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: got %d ids %v, want %d ids %v", label, len(got), got, len(want), want)
	}
	wantSet := make(map[string]bool, len(want))
	for _, id := range want {
		wantSet[id] = true
	}
	for _, id := range got {
		if !wantSet[id] {
			t.Errorf("%s: unexpected id %q in result %v (want %v)", label, id, got, want)
		}
	}
}
