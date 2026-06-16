package mcpserver

// control_test.go — D-071 Tier control verbs on the MCP surface: memory_flush,
// memory_branch (fork/merge/discard), and the Tier-B memory_grants tool.

import (
	"context"
	"testing"

	"github.com/hurtener/stowage/internal/grants"
)

func TestFlushHandler(t *testing.T) {
	svc := newHandlerServices(t) // PipelineStage nil
	ctx := context.Background()
	h := makeFlushHandler(svc)

	res, err := h(ctx, FlushInput{Key: "sess/branch", Trigger: "explicit"})
	if err != nil {
		t.Fatalf("flush: %v", err)
	}
	if res.Structured.Key != "sess/branch" || res.Structured.Trigger != "explicit" {
		t.Errorf("flush echo wrong: %+v", res.Structured)
	}
	// Empty key + bad trigger validation.
	if _, err := h(ctx, FlushInput{Key: ""}); err == nil {
		t.Error("flush empty key: expected error")
	}
	if _, err := h(ctx, FlushInput{Key: "k", Trigger: "bogus"}); err == nil {
		t.Error("flush bad trigger: expected error")
	}
}

func TestBranchHandler(t *testing.T) {
	svc := newHandlerServices(t)
	ctx := context.Background()
	h := makeBranchHandler(svc)

	fork, err := h(ctx, BranchInput{Action: "fork", SessionID: "sess-1"})
	if err != nil {
		t.Fatalf("fork: %v", err)
	}
	id := fork.Structured.BranchID
	if id == "" || fork.Structured.Status != "open" {
		t.Fatalf("fork unexpected: %+v", fork.Structured)
	}

	merged, err := h(ctx, BranchInput{Action: "merge", BranchID: id})
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if merged.Structured.Status != "merged" {
		t.Errorf("merge status: %q", merged.Structured.Status)
	}

	// Fork another and discard it.
	f2, err := h(ctx, BranchInput{Action: "discard", SessionID: "ignored", BranchID: ""})
	if err == nil {
		t.Errorf("discard without branch_id: expected error, got %+v", f2.Structured)
	}
	f3, _ := h(ctx, BranchInput{Action: "fork", SessionID: "sess-2"})
	disc, err := h(ctx, BranchInput{Action: "discard", BranchID: f3.Structured.BranchID})
	if err != nil {
		t.Fatalf("discard: %v", err)
	}
	if disc.Structured.Status != "discarded" {
		t.Errorf("discard status: %q", disc.Structured.Status)
	}

	// Validation.
	if _, err := h(ctx, BranchInput{Action: "fork"}); err == nil {
		t.Error("fork without session_id: expected error")
	}
	if _, err := h(ctx, BranchInput{Action: "bogus"}); err == nil {
		t.Error("unknown action: expected error")
	}
}

func TestGrantsHandler(t *testing.T) {
	st := newHandlerStore(t)
	log := noopLog()
	svc := &Services{
		Store:     st,
		GrantsSvc: grants.New(st.Grants(), st.Events(), log),
		Log:       log,
		ScopeFn:   StdioScopeFn("grants-tenant"),
	}
	ctx := context.Background()
	h := makeGrantsHandler(svc)

	// create_group + list_groups.
	cg, err := h(ctx, GrantsInput{Action: "create_group", Name: "team"})
	if err != nil {
		t.Fatalf("create_group: %v", err)
	}
	gid := cg.Structured.Group.ID
	if gid == "" {
		t.Fatal("create_group: empty group id")
	}
	lg, _ := h(ctx, GrantsInput{Action: "list_groups"})
	if len(lg.Structured.Groups) != 1 {
		t.Errorf("list_groups: want 1 got %d", len(lg.Structured.Groups))
	}

	// add_member + list_members.
	if _, err := h(ctx, GrantsInput{Action: "add_member", GroupID: gid, UserID: "alice"}); err != nil {
		t.Fatalf("add_member: %v", err)
	}
	lm, _ := h(ctx, GrantsInput{Action: "list_members", GroupID: gid})
	if len(lm.Structured.Members) != 1 {
		t.Errorf("list_members: want 1 got %d", len(lm.Structured.Members))
	}

	// create_grant + list_grants + revoke_grant.
	cgr, err := h(ctx, GrantsInput{
		Action: "create_grant", GroupID: gid, UserID: "bob",
		Access: "contribute", ZoneCeiling: "work",
	})
	if err != nil {
		t.Fatalf("create_grant: %v", err)
	}
	grantID := cgr.Structured.Grant.ID
	if grantID == "" {
		t.Fatal("create_grant: empty grant id")
	}
	lgr, _ := h(ctx, GrantsInput{Action: "list_grants"})
	if len(lgr.Structured.Grants) != 1 {
		t.Errorf("list_grants: want 1 got %d", len(lgr.Structured.Grants))
	}
	rv, err := h(ctx, GrantsInput{Action: "revoke_grant", GrantID: grantID})
	if err != nil {
		t.Fatalf("revoke_grant: %v", err)
	}
	if rv.Structured.Revoked != grantID {
		t.Errorf("revoke_grant: revoked %q want %q", rv.Structured.Revoked, grantID)
	}

	// remove_member.
	if _, err := h(ctx, GrantsInput{Action: "remove_member", GroupID: gid, UserID: "alice"}); err != nil {
		t.Fatalf("remove_member: %v", err)
	}

	// Validation: unknown action + missing required fields.
	if _, err := h(ctx, GrantsInput{Action: "create_group"}); err == nil {
		t.Error("create_group without name: expected error")
	}
	if _, err := h(ctx, GrantsInput{Action: "create_grant", GroupID: gid}); err == nil {
		t.Error("create_grant without zone_ceiling: expected error")
	}
	if _, err := h(ctx, GrantsInput{Action: "bogus"}); err == nil {
		t.Error("unknown action: expected error")
	}

	// Nil grants service → error.
	svc.GrantsSvc = nil
	if _, err := h(ctx, GrantsInput{Action: "list_groups"}); err == nil {
		t.Error("nil grants svc: expected error")
	}
}
