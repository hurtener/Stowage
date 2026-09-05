package conformance

import (
	"context"
	"errors"
	"testing"

	"github.com/oklog/ulid/v2"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/store"
)

// RunCommands checks the optional command guard at the actual store boundary.
// Failed guards must roll back their receipt reservation as well as all effects.
// Both SQL drivers run this same contract in their own instrumented test suites.
func RunCommands(t *testing.T, st store.Store) {
	t.Helper()
	ctx := context.Background()
	owner := identity.Scope{Tenant: ulid.Make().String(), Project: "project", User: "owner"}
	leaf := owner
	leaf.Session = "session"
	sourceID := ulid.Make().String()
	if err := st.Records().Append(ctx, leaf, []store.Record{{ID: sourceID, Role: "user", Content: "Keep authentication in Pengui.", OccurredAt: 100, CreatedAt: 100}}); err != nil {
		t.Fatal(err)
	}
	source, err := st.Records().Get(ctx, leaf, sourceID)
	if err != nil {
		t.Fatal(err)
	}
	targetID := ulid.Make().String()
	if err := st.Memories().Insert(ctx, leaf, store.Memory{ID: targetID, Kind: "decision", Content: "Earlier decision", Status: "active", CreatedAt: 100, UpdatedAt: 100}); err != nil {
		t.Fatal(err)
	}
	target, err := st.Memories().Get(ctx, leaf, targetID)
	if err != nil {
		t.Fatal(err)
	}
	command := func() store.CommitSet {
		return store.CommitSet{Action: store.ActionDiscard, Command: &store.CommandGuard{
			Receipt: store.Event{ID: ulid.Make().String(), Type: "memory.command_committed", SubjectID: targetID, Reason: "command conformance", Payload: "{}", CreatedAt: 200},
			Source:  *source,
			Targets: []store.Memory{*target},
		}}
	}
	cs := command()
	if err := st.Memories().Commit(ctx, owner, cs); err != nil {
		t.Fatalf("valid command: %v", err)
	}
	ev, err := st.Events().Get(ctx, owner, cs.Command.Receipt.ID)
	if err != nil || ev.SubjectID != targetID || ev.ProjectID != owner.Project || ev.UserID != owner.User {
		t.Fatalf("receipt lost or mis-scoped: %+v %v", ev, err)
	}
	if err := st.Memories().Commit(ctx, owner, cs); !errors.Is(err, store.ErrCommandReplay) {
		t.Fatalf("duplicate receipt was not rejected: %v", err)
	}
	for _, wrong := range []identity.Scope{{Tenant: owner.Tenant}, {Tenant: owner.Tenant, Project: owner.Project, User: "other"}, leaf} {
		if _, err := st.Events().Get(ctx, wrong, cs.Command.Receipt.ID); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("receipt lookup ignored its exact leaf: %v", err)
		}
	}
	if _, err := st.Events().Get(ctx, identity.Scope{}, cs.Command.Receipt.ID); !errors.Is(err, store.ErrScopeRequired) {
		t.Errorf("unscoped receipt lookup: %v", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := st.Events().Get(cancelled, owner, cs.Command.Receipt.ID); err == nil {
		t.Error("cancelled receipt lookup succeeded")
	}

	cases := []struct {
		name   string
		change func(*store.CommandGuard)
	}{
		{"missing_receipt", func(g *store.CommandGuard) { g.Receipt.ID = "" }},
		{"missing_source_id", func(g *store.CommandGuard) { g.Source.ID = "" }},
		{"missing_source", func(g *store.CommandGuard) { g.Source.ID = ulid.Make().String() }},
		{"foreign_source_tenant", func(g *store.CommandGuard) { g.Source.TenantID = "foreign" }},
		{"foreign_source_user", func(g *store.CommandGuard) { g.Source.UserID = "foreign" }},
		{"source_content_changed", func(g *store.CommandGuard) { g.Source.Content = "fabricated evidence" }},
		{"source_time_changed", func(g *store.CommandGuard) { g.Source.OccurredAt++ }},
		{"source_role_changed", func(g *store.CommandGuard) { g.Source.Role = "assistant" }},
		{"missing_target", func(g *store.CommandGuard) { g.Targets[0].ID = ulid.Make().String() }},
		{"foreign_target", func(g *store.CommandGuard) { g.Targets[0].UserID = "foreign" }},
		{"stale_target", func(g *store.CommandGuard) { g.Targets[0].Content = "stale snapshot" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			attempt := command()
			tc.change(attempt.Command)
			if err := st.Memories().Commit(ctx, owner, attempt); !errors.Is(err, store.ErrCommandConflict) {
				t.Fatalf("guard did not fail closed: %v", err)
			}
			if _, err := st.Events().Get(ctx, owner, attempt.Command.Receipt.ID); !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("failed command left a durable receipt: %v", err)
			}
		})
	}

	// A later mutation error also rolls back the earlier receipt reservation.
	bad := command()
	bad.Action = store.ActionAdd
	bad.Memory = *target // duplicate primary key, after a valid guard
	if err := st.Memories().Commit(ctx, owner, bad); err == nil {
		t.Fatal("duplicate mutation unexpectedly succeeded")
	}
	if _, err := st.Events().Get(ctx, owner, bad.Command.Receipt.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("failed mutation reserved the idempotency key: %v", err)
	}
	// The same key remains usable after the failed transaction.
	bad.Action = store.ActionDiscard
	if err := st.Memories().Commit(ctx, owner, bad); err != nil {
		t.Fatalf("rolled-back key was not reusable: %v", err)
	}
}
