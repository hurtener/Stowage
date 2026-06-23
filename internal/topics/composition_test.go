package topics_test

import (
	"context"
	"strconv"
	"testing"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/topics"
)

// keysOf returns the topic keys in order.
func keysOf(views []topics.TopicView) []string {
	ks := make([]string, len(views))
	for i, v := range views {
		ks[i] = v.Key
	}
	return ks
}

// TestUpsert_ReservedPackNamespace: the pack: namespace is reserved — pack:off and
// pack:on:<name> are accepted, but a bare pack name as an explicit topic is rejected
// (D-099 footgun guard).
func TestUpsert_ReservedPackNamespace(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	svc := topics.New(st.Topics(), noopLog(), "assistant")
	scope := identity.Scope{Tenant: "t-reserved"}

	// Accepted sentinels.
	for _, key := range []string{topics.PackOff, "pack:on:project", "pack:on:bogus"} {
		if _, err := svc.Upsert(context.Background(), scope, []topics.TopicUpsert{{Key: key, Status: "active"}}); err != nil {
			t.Errorf("Upsert(%q) should be accepted, got %v", key, err)
		}
	}
	// Rejected: a bare pack name used as an explicit topic.
	for _, key := range []string{"pack:project", "pack:preferences", "pack:whatever"} {
		_, err := svc.Upsert(context.Background(), scope, []topics.TopicUpsert{{Key: key, Description: "d", Status: "active"}})
		if err == nil {
			t.Errorf("Upsert(%q) should be rejected (reserved namespace)", key)
		}
	}
}

// TestResolve_Union_ExplicitFirst_ExplicitWins composes an enabled pack with two
// explicit topics: explicit come first and win any key collision (D-099).
func TestResolve_Union_ExplicitFirst_ExplicitWins(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	svc := topics.New(st.Topics(), noopLog(), "assistant")
	scope := identity.Scope{Tenant: "t-union"}

	// One bespoke topic, plus a topic whose key COLLIDES with a project-pack entry
	// (project-glossary) — explicit must win — plus the pack:on sentinel.
	upsertTopic(t, st.Topics(), scope, "billing-flow", "how billing works", "active")
	upsertTopic(t, st.Topics(), scope, "project-glossary", "MY override of the glossary topic", "active")
	upsertTopic(t, st.Topics(), scope, "pack:on:project", "", "active")

	r, err := svc.Resolve(context.Background(), scope)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	// Explicit topics first (store order), then project-pack entries (minus the
	// collided key which explicit owns).
	if len(r.Topics) < 3 {
		t.Fatalf("want composed set, got %d: %v", len(r.Topics), keysOf(r.Topics))
	}
	if r.Topics[0].Key != "billing-flow" || r.Topics[0].Source != "explicit" {
		t.Errorf("first should be explicit billing-flow, got %q/%q", r.Topics[0].Key, r.Topics[0].Source)
	}
	// project-glossary appears exactly once and is the explicit override.
	var glossary []topics.TopicView
	for _, v := range r.Topics {
		if v.Key == "project-glossary" {
			glossary = append(glossary, v)
		}
	}
	if len(glossary) != 1 {
		t.Fatalf("project-glossary must appear once (explicit wins), got %d", len(glossary))
	}
	if glossary[0].Source != "explicit" || glossary[0].Description != "MY override of the glossary topic" {
		t.Errorf("project-glossary should be the explicit override, got source=%q desc=%q",
			glossary[0].Source, glossary[0].Description)
	}
	// At least one project-pack entry made it in, tagged with the pack source.
	sawProjectPack := false
	for _, v := range r.Topics {
		if v.Source == topics.PackProject {
			sawProjectPack = true
			if v.Pack != topics.PackProject {
				t.Errorf("pack entry %q: Pack=%q want %q", v.Key, v.Pack, topics.PackProject)
			}
		}
	}
	if !sawProjectPack {
		t.Errorf("expected at least one pack:project entry in the union, got %v", keysOf(r.Topics))
	}
}

// TestResolve_PackOn_Multi enables two packs and confirms both contribute, in
// enable order, with no default pack auto-added.
func TestResolve_PackOn_Multi(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	svc := topics.New(st.Topics(), noopLog(), "assistant")
	scope := identity.Scope{Tenant: "t-multi"}

	upsertTopic(t, st.Topics(), scope, "pack:on:project", "", "active")
	upsertTopic(t, st.Topics(), scope, "pack:on:incidents", "", "active")

	views, err := svc.ActiveTopics(context.Background(), scope)
	if err != nil {
		t.Fatalf("ActiveTopics: %v", err)
	}
	var sawProject, sawIncidents, sawPreferences bool
	for _, v := range views {
		switch v.Source {
		case topics.PackProject:
			sawProject = true
		case topics.PackIncidents:
			sawIncidents = true
		case topics.PackPreferences:
			sawPreferences = true
		}
	}
	if !sawProject || !sawIncidents {
		t.Errorf("want both project + incidents packs; got %v", keysOf(views))
	}
	if sawPreferences {
		t.Errorf("default pack:preferences must NOT be auto-added once a pack is enabled")
	}
}

// TestResolve_DefaultPack_OnlyWhenNoIntent: a coding-agent scope that enables one
// pack gets ONLY that pack, not the profile default (agent-learnings).
func TestResolve_DefaultPack_OnlyWhenNoIntent(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	svc := topics.New(st.Topics(), noopLog(), "coding-agent")
	scope := identity.Scope{Tenant: "t-intent"}

	upsertTopic(t, st.Topics(), scope, "pack:on:research", "", "active")

	views, err := svc.ActiveTopics(context.Background(), scope)
	if err != nil {
		t.Fatalf("ActiveTopics: %v", err)
	}
	if len(views) == 0 {
		t.Fatal("want the research pack, got none")
	}
	for _, v := range views {
		if v.Source != topics.PackResearch {
			t.Errorf("want only research pack (no profile default), got %q from %q", v.Key, v.Source)
		}
	}
}

// TestResolve_UnknownPackOn_Ignored: pack:on:bogus is ignored (not an explicit
// topic, not an error) and does not suppress the default pack.
func TestResolve_UnknownPackOn_Ignored(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	svc := topics.New(st.Topics(), noopLog(), "assistant")
	scope := identity.Scope{Tenant: "t-bogus"}

	upsertTopic(t, st.Topics(), scope, "pack:on:does-not-exist", "", "active")

	views, err := svc.ActiveTopics(context.Background(), scope)
	if err != nil {
		t.Fatalf("ActiveTopics: %v", err)
	}
	// Unknown pack:on is not an explicit topic and not a known pack → the scope
	// expressed no usable intent → the assistant default pack applies.
	for _, v := range views {
		if v.Key == "pack:on:does-not-exist" {
			t.Errorf("unknown pack:on sentinel must not appear as a topic")
		}
		if v.Source != topics.PackPreferences {
			t.Errorf("want default pack:preferences (unknown pack:on ignored), got %q", v.Source)
		}
	}
	if len(views) == 0 {
		t.Errorf("expected the default pack to apply when only an unknown pack:on is present")
	}
}

// TestResolve_PackOff_SuppressesPacksKeepsExplicit: pack:off + pack:on + explicit →
// only the explicit topic survives (pack:off dominates the pack layer, D-099).
func TestResolve_PackOff_SuppressesPacksKeepsExplicit(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	svc := topics.New(st.Topics(), noopLog(), "assistant")
	scope := identity.Scope{Tenant: "t-off-keeps"}

	upsertTopic(t, st.Topics(), scope, "only-this", "the one explicit topic", "active")
	upsertTopic(t, st.Topics(), scope, "pack:on:project", "", "active")
	upsertTopic(t, st.Topics(), scope, topics.PackOff, "", "active")

	views, err := svc.ActiveTopics(context.Background(), scope)
	if err != nil {
		t.Fatalf("ActiveTopics: %v", err)
	}
	if len(views) != 1 || views[0].Key != "only-this" || views[0].Source != "explicit" {
		t.Errorf("pack:off must suppress packs but keep explicit; got %v", keysOf(views))
	}
}

// TestResolve_Cap enables every pack so the union exceeds MaxActiveTopics, and
// asserts the cap keeps exactly MaxActiveTopics, drops pack entries (never the
// explicit topic), and reports DroppedKeys.
func TestResolve_Cap(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	svc := topics.New(st.Topics(), noopLog(), "assistant")
	scope := identity.Scope{Tenant: "t-cap"}

	upsertTopic(t, st.Topics(), scope, "keep-me", "explicit, must never be dropped", "active")
	for _, p := range []string{
		"pack:on:preferences", "pack:on:agent-learnings", "pack:on:project",
		"pack:on:incidents", "pack:on:product", "pack:on:people",
		"pack:on:compliance", "pack:on:research",
	} {
		upsertTopic(t, st.Topics(), scope, p, "", "active")
	}

	r, err := svc.Resolve(context.Background(), scope)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(r.Topics) != topics.MaxActiveTopics {
		t.Errorf("want exactly %d topics after cap, got %d", topics.MaxActiveTopics, len(r.Topics))
	}
	if len(r.DroppedKeys) == 0 {
		t.Errorf("want DroppedKeys populated when the union exceeds the cap")
	}
	// The explicit topic must survive and lead.
	if r.Topics[0].Key != "keep-me" || r.Topics[0].Source != "explicit" {
		t.Errorf("explicit topic must be kept and first; got %q/%q", r.Topics[0].Key, r.Topics[0].Source)
	}
	// Completeness + no-loss + no-dup: kept ∪ dropped is a partition of the composed
	// set; "keep-me" is kept and never dropped; every dropped key is a distinct
	// non-explicit key. With the deterministic (created_at, key) List order this drop
	// set is reproducible across drivers/runs.
	seen := map[string]bool{}
	for _, v := range r.Topics {
		if seen[v.Key] {
			t.Errorf("duplicate key in kept set: %q", v.Key)
		}
		seen[v.Key] = true
	}
	for _, k := range r.DroppedKeys {
		if k == "keep-me" {
			t.Errorf("explicit topic must never be dropped by the cap")
		}
		if seen[k] {
			t.Errorf("dropped key %q also present in kept set", k)
		}
		seen[k] = true
	}
	if !seen["keep-me"] {
		t.Errorf("explicit topic missing from the composed set")
	}
}

// TestResolve_ExplicitExceedsCap: when explicit topics alone exceed MaxActiveTopics,
// ALL explicit topics are kept (the result exceeds the cap) and every pack entry is
// dropped — "explicit topics are never dropped" (D-099).
func TestResolve_ExplicitExceedsCap(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	svc := topics.New(st.Topics(), noopLog(), "assistant")
	scope := identity.Scope{Tenant: "t-explicit-over"}

	n := topics.MaxActiveTopics + 5
	for i := 0; i < n; i++ {
		upsertTopic(t, st.Topics(), scope, "x-topic-"+strconv.Itoa(i), "explicit", "active")
	}
	upsertTopic(t, st.Topics(), scope, "pack:on:project", "", "active")

	r, err := svc.Resolve(context.Background(), scope)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(r.Topics) != n {
		t.Errorf("all %d explicit topics must be kept (result may exceed the cap), got %d", n, len(r.Topics))
	}
	for _, v := range r.Topics {
		if v.Source != "explicit" {
			t.Errorf("no pack entry should survive when explicit alone exceed the cap; got %q (%q)", v.Key, v.Source)
		}
	}
	if len(r.DroppedKeys) == 0 {
		t.Errorf("the project pack's entries should all be in DroppedKeys")
	}
}

// TestResolve_EachCuratedPackResolves enables each curated pack on its own and
// asserts it resolves to a non-empty set all tagged with that pack's source — proving
// every shipped pack is registered and renders (AC-6).
func TestResolve_EachCuratedPackResolves(t *testing.T) {
	t.Parallel()
	packs := map[string]string{
		"pack:on:preferences":     topics.PackPreferences,
		"pack:on:agent-learnings": topics.PackAgentLearnings,
		"pack:on:project":         topics.PackProject,
		"pack:on:incidents":       topics.PackIncidents,
		"pack:on:product":         topics.PackProduct,
		"pack:on:people":          topics.PackPeople,
		"pack:on:compliance":      topics.PackCompliance,
		"pack:on:research":        topics.PackResearch,
	}
	for sentinel, packName := range packs {
		sentinel, packName := sentinel, packName
		t.Run(packName, func(t *testing.T) {
			t.Parallel()
			st := newTestStore(t)
			svc := topics.New(st.Topics(), noopLog(), "assistant")
			scope := identity.Scope{Tenant: "t-" + packName}
			upsertTopic(t, st.Topics(), scope, sentinel, "", "active")

			views, err := svc.ActiveTopics(context.Background(), scope)
			if err != nil {
				t.Fatalf("ActiveTopics: %v", err)
			}
			if len(views) == 0 {
				t.Fatalf("pack %q resolved to no topics", packName)
			}
			for _, v := range views {
				if v.Source != packName || v.Pack != packName {
					t.Errorf("entry %q: source=%q pack=%q, want %q", v.Key, v.Source, v.Pack, packName)
				}
				if v.Key == "" || v.Description == "" {
					t.Errorf("pack %q has an entry with empty key/description: %+v", packName, v)
				}
			}
		})
	}
}
