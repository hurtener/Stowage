package topics_test

import (
	"context"
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
	for _, k := range r.DroppedKeys {
		if k == "keep-me" {
			t.Errorf("explicit topic must never be dropped by the cap")
		}
	}
}
