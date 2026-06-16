// surface_parity_test.go proves the Phase h4 (D-071) both-surfaces-identical
// bar: each Tier-A control verb (topic upsert/delete, buffer flush, branch
// fork/merge/discard) drives to the SAME observable effect whether invoked
// through the embedded SDK or the HTTP server, both over real sqlite. Runs under
// -race.
//
// memory_assert is Tier A on {SDK, MCP} but deliberately NOT on HTTP (D-071); the
// SDK-level parity suite (sdk/stowage/suite_test.go) covers it on both impls. The
// MCP contribute-mode honoring (AC-3) is proven in internal/mcpserver against a
// real store + grants service.
package integration

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hurtener/stowage/internal/api"
	"github.com/hurtener/stowage/internal/auth"
	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/store"
	stowage "github.com/hurtener/stowage/sdk/stowage"
)

// tierAOutcome captures the observable effect of each Tier-A verb for comparison.
type tierAOutcome struct {
	topicPresentAfterUpsert bool
	topicAbsentAfterDelete  bool
	flushTrigger            string
	flushFlushed            bool
	forkNonEmpty            bool
	mergeStatus             string // read back from the store
	discardStatus           string // read back from the store
}

// runTierA exercises every Tier-A verb against client and reads the resulting
// branch state back from st (the same store the client writes through).
func runTierA(t *testing.T, client stowage.Client, st store.Store, scope identity.Scope) tierAOutcome {
	t.Helper()
	ctx := context.Background()
	var out tierAOutcome

	// ── topic upsert / delete ──
	const topicKey = "parity-topic"
	if _, err := client.UpsertTopics(ctx, stowage.UpsertTopicsRequest{
		Topics: []stowage.TopicUpsert{{Key: topicKey, Description: "parity"}},
	}); err != nil {
		t.Fatalf("UpsertTopics: %v", err)
	}
	topics, err := client.Topics(ctx)
	if err != nil {
		t.Fatalf("Topics after upsert: %v", err)
	}
	for _, tv := range topics.Topics {
		if tv.Key == topicKey {
			out.topicPresentAfterUpsert = true
		}
	}
	if _, err := client.DeleteTopic(ctx, topicKey); err != nil {
		t.Fatalf("DeleteTopic: %v", err)
	}
	topics, err = client.Topics(ctx)
	if err != nil {
		t.Fatalf("Topics after delete: %v", err)
	}
	out.topicAbsentAfterDelete = true
	for _, tv := range topics.Topics {
		if tv.Key == topicKey {
			out.topicAbsentAfterDelete = false
		}
	}

	// ── buffer flush ──
	flush, err := client.Flush(ctx, stowage.FlushRequest{Key: "parity/flush", Trigger: "explicit"})
	if err != nil {
		t.Fatalf("Flush: %v", err)
	}
	out.flushTrigger = flush.Trigger
	out.flushFlushed = flush.Flushed

	// ── branch fork / merge / discard ──
	fork, err := client.ForkBranch(ctx, stowage.ForkBranchRequest{SessionID: "parity-sess"})
	if err != nil {
		t.Fatalf("ForkBranch: %v", err)
	}
	out.forkNonEmpty = fork.BranchID != ""

	if _, err := client.MergeBranch(ctx, fork.BranchID); err != nil {
		t.Fatalf("MergeBranch: %v", err)
	}
	merged, err := st.Branches().Get(ctx, scope, fork.BranchID)
	if err != nil {
		t.Fatalf("Get merged branch: %v", err)
	}
	out.mergeStatus = merged.Status

	fork2, err := client.ForkBranch(ctx, stowage.ForkBranchRequest{SessionID: "parity-sess-2"})
	if err != nil {
		t.Fatalf("ForkBranch 2: %v", err)
	}
	if _, err := client.DiscardBranch(ctx, fork2.BranchID); err != nil {
		t.Fatalf("DiscardBranch: %v", err)
	}
	discarded, err := st.Branches().Get(ctx, scope, fork2.BranchID)
	if err != nil {
		t.Fatalf("Get discarded branch: %v", err)
	}
	out.discardStatus = discarded.Status

	return out
}

func runTierAEmbedded(t *testing.T) tierAOutcome {
	t.Helper()
	cfg := baseConfig(t)
	tenant := "h4-embedded"
	scope := identity.Scope{Tenant: tenant}
	ctx := context.Background()

	client, closer, err := stowage.NewEmbedded(ctx, cfg, stowage.WithTenantID(tenant))
	if err != nil {
		t.Fatalf("NewEmbedded: %v", err)
	}
	t.Cleanup(func() {
		shutCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = closer(shutCtx)
	})

	// A side store on the same DSN reads back branch state (the SDK exposes no
	// raw branch reads).
	side, err := store.Open(ctx, cfg.Store)
	if err != nil {
		t.Fatalf("side store open: %v", err)
	}
	defer func() { _ = side.Close(ctx) }()

	return runTierA(t, client, side, scope)
}

func runTierAServe(t *testing.T) tierAOutcome {
	t.Helper()
	cfg := baseConfig(t)
	tenant := "h4-serve"
	scope := identity.Scope{Tenant: tenant}

	stk, p := startStack(t, cfg)

	srv, err := api.New(&cfg, stk.Store, stk.Log, stk.Metrics)
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	srv.SetPipelineIn(p.In)
	srv.SetStage(p.Stage)
	srv.SetTopicService(stk.TopicSvc)
	srv.SetRetriever(stk.Retriever)
	srv.SetGrantsService(stk.GrantsSvc)

	ts := httptest.NewServer(srv)
	t.Cleanup(func() {
		ts.Close()
		shutCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
		_ = p.Drain(shutCtx)
		_ = stk.Close(shutCtx)
	})

	key, plaintext, err := auth.Generate(tenant, auth.RoleAgent)
	if err != nil {
		t.Fatalf("auth.Generate: %v", err)
	}
	if err := stk.Store.Keys().Insert(key); err != nil {
		t.Fatalf("keys insert: %v", err)
	}

	client := stowage.NewHTTP(ts.URL, plaintext)
	return runTierA(t, client, stk.Store, scope)
}

// TestSurfaceParity_TierA_EmbeddedVsServe is the AC-4 both-surfaces-identical bar.
func TestSurfaceParity_TierA_EmbeddedVsServe(t *testing.T) {
	emb := runTierAEmbedded(t)
	srv := runTierAServe(t)

	if emb != srv {
		t.Errorf("Tier-A observable effect diverges:\n embedded=%+v\n    serve=%+v", emb, srv)
	}
	// And the effects are the expected ones (not identically broken).
	if !emb.topicPresentAfterUpsert || !emb.topicAbsentAfterDelete {
		t.Errorf("topic upsert/delete effect wrong: %+v", emb)
	}
	if emb.flushTrigger != "explicit" || !emb.flushFlushed {
		t.Errorf("flush effect wrong: %+v", emb)
	}
	if !emb.forkNonEmpty || emb.mergeStatus != "merged" || emb.discardStatus != "discarded" {
		t.Errorf("branch effect wrong: %+v", emb)
	}
}
