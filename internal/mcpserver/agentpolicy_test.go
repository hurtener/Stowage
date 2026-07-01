package mcpserver

// agentpolicy_test.go — Phase ae1 (D-135/D-146/D-151) coverage: the
// memory_agent_policy admin tool (create/get/list/delete) and the
// _meta.agent_id -> Scope.Agent read-time seam on memory_retrieve
// (server.RequestMeta, dockyard v1.8.0, M5).

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/hurtener/dockyard/runtime/server"
	"github.com/oklog/ulid/v2"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/hurtener/stowage/internal/config"
	"github.com/hurtener/stowage/internal/gateway"
	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/retrieval"
	"github.com/hurtener/stowage/internal/store"
	"github.com/hurtener/stowage/internal/topics"
	"github.com/hurtener/stowage/internal/vindex"
)

// commitTopicTaggedMemory inserts an active memory tagged with topics, directly
// through the store (mirrors internal/retrieval's topicfilter_test.go helper) so
// the agent filter has real memory_topics junction rows to subtract on.
func commitTopicTaggedMemory(t *testing.T, st store.Store, scope identity.Scope, content string, topics []string) string {
	t.Helper()
	id := ulid.Make().String()
	now := time.Now().UnixMilli()
	cs := store.CommitSet{
		Action: store.ActionAdd,
		Memory: store.Memory{
			ID: id, Kind: "fact", Content: content, Status: "active",
			Importance: 3, Confidence: 0.8, TrustSource: "llm_extracted", Stability: 1.0,
			PrivacyZone: "public", CreatedAt: now, UpdatedAt: now,
		},
		Topics: topics,
		Scope:  scope,
	}
	if err := st.Memories().Commit(context.Background(), scope, cs); err != nil {
		t.Fatalf("commitTopicTaggedMemory: %v", err)
	}
	return id
}

// newAgentPolicyServices builds a Services with a real Retriever wired for the
// agent filter (retrieval.agent_views.enabled=true equivalent), so both
// memory_agent_policy and memory_retrieve's _meta seam can be exercised
// end-to-end against a real sqlite store.
func newAgentPolicyServices(t *testing.T) *Services {
	t.Helper()
	st := newHandlerStore(t)
	log := noopLog()

	gw, err := gateway.Open(context.Background(), config.GatewayConfig{
		Driver:    "mock",
		EmbedDims: 8,
	}, slog.Default(), prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("open mock gateway: %v", err)
	}
	t.Cleanup(func() { _ = gw.Close(context.Background()) })

	vi := vindex.New(st.Vectors(), 8, "mock-embed")
	ret := retrieval.New(st.Memories(), st.Records(), vi, gw, log).WithAgentPolicy(st.TopicViews(), true)
	topicSvc := topics.New(st.Topics(), log, "assistant")

	return &Services{
		Store:      st,
		Retriever:  ret,
		Gateway:    gw,
		TopicSvc:   topicSvc,
		PipelineIn: nil,
		Log:        log,
		ScopeFn:    StdioScopeFn("agent-policy-tenant"),
	}
}

func TestAgentPolicyHandler_CRUD(t *testing.T) {
	svc := newAgentPolicyServices(t)
	ctx := context.Background()
	h := makeAgentPolicyHandler(svc)

	// create.
	created, err := h(ctx, AgentPolicyInput{
		Action: "create", AgentID: "agent-1",
		AllowTopics: []string{"auth", "billing"}, DenyTopics: []string{"secrets"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.Structured.Policy == nil || created.Structured.Policy.AgentID != "agent-1" {
		t.Fatalf("create: unexpected policy: %+v", created.Structured.Policy)
	}

	// get.
	got, err := h(ctx, AgentPolicyInput{Action: "get", AgentID: "agent-1"})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Structured.Policy == nil || len(got.Structured.Policy.AllowTopics) != 2 {
		t.Fatalf("get: unexpected policy: %+v", got.Structured.Policy)
	}

	// list.
	if _, err := h(ctx, AgentPolicyInput{Action: "create", AgentID: "agent-2", AllowTopics: []string{"x"}}); err != nil {
		t.Fatalf("create agent-2: %v", err)
	}
	listed, err := h(ctx, AgentPolicyInput{Action: "list"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(listed.Structured.Policies) != 2 {
		t.Fatalf("list: want 2 policies, got %d", len(listed.Structured.Policies))
	}

	// re-create (atomic replace).
	replaced, err := h(ctx, AgentPolicyInput{Action: "create", AgentID: "agent-1", AllowTopics: []string{"only-this"}})
	if err != nil {
		t.Fatalf("replace: %v", err)
	}
	if len(replaced.Structured.Policy.AllowTopics) != 1 || replaced.Structured.Policy.AllowTopics[0] != "only-this" {
		t.Fatalf("replace: unexpected policy: %+v", replaced.Structured.Policy)
	}
	if len(replaced.Structured.Policy.DenyTopics) != 0 {
		t.Errorf("replace: DenyTopics must be fully replaced (empty), got %v", replaced.Structured.Policy.DenyTopics)
	}

	// delete.
	deleted, err := h(ctx, AgentPolicyInput{Action: "delete", AgentID: "agent-1"})
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if deleted.Structured.Deleted != "agent-1" {
		t.Errorf("delete: got %q want agent-1", deleted.Structured.Deleted)
	}
	if _, err := h(ctx, AgentPolicyInput{Action: "get", AgentID: "agent-1"}); err == nil {
		t.Error("get after delete: expected ErrNotFound-shaped error")
	}
}

func TestAgentPolicyHandler_Validation(t *testing.T) {
	svc := newAgentPolicyServices(t)
	ctx := context.Background()
	h := makeAgentPolicyHandler(svc)

	if _, err := h(ctx, AgentPolicyInput{Action: "create"}); err == nil {
		t.Error("create without agent_id: expected error")
	}
	if _, err := h(ctx, AgentPolicyInput{Action: "get"}); err == nil {
		t.Error("get without agent_id: expected error")
	}
	if _, err := h(ctx, AgentPolicyInput{Action: "delete"}); err == nil {
		t.Error("delete without agent_id: expected error")
	}
	if _, err := h(ctx, AgentPolicyInput{Action: "bogus"}); err == nil {
		t.Error("unknown action: expected error")
	}

	// Nil retriever → error (mirrors TestGrantsHandler's nil-service branch).
	svc2 := newHandlerServices(t) // Retriever is nil
	h2 := makeAgentPolicyHandler(svc2)
	if _, err := h2(ctx, AgentPolicyInput{Action: "list"}); err == nil {
		t.Error("nil retriever: expected error")
	}
}

// TestHandlerRetrieve_MetaAgentIDSeam proves the M5 wiring end to end: a host
// injecting _meta.agent_id (via dockyard v1.8.0's server.RequestMeta) narrows
// memory_retrieve's own-scope results according to the bound agent's policy —
// and DegradedAgentFilter surfaces false on a clean, filtered read.
func TestHandlerRetrieve_MetaAgentIDSeam(t *testing.T) {
	svc := newAgentPolicyServices(t)
	ctx := context.Background()

	polH := makeAgentPolicyHandler(svc)
	if _, err := polH(ctx, AgentPolicyInput{
		Action: "create", AgentID: "bound-agent", AllowTopics: []string{"auth"},
	}); err != nil {
		t.Fatalf("create policy: %v", err)
	}

	scope := identity.Scope{Tenant: "agent-policy-tenant"}
	commitTopicTaggedMemory(t, svc.Store, scope, "auth widget note qvzxm", []string{"auth"})
	commitTopicTaggedMemory(t, svc.Store, scope, "deploy widget note qvzxm", []string{"deploy"})

	retrieveH := makeRetrieveHandler(svc)

	// No _meta -> Scope.Agent empty -> unfiltered (inert, zero-config).
	unfiltered, err := retrieveH(ctx, RetrieveInput{Query: "qvzxm widget", Limit: 10})
	if err != nil {
		t.Fatalf("retrieve (no meta): %v", err)
	}
	if len(unfiltered.Structured.Items) < 2 {
		t.Fatalf("expected both memories with no agent identity, got %d", len(unfiltered.Structured.Items))
	}

	// _meta.agent_id = bound-agent -> narrowed to the allow-topic memory only.
	metaCtx := server.WithRequestMeta(ctx, map[string]any{"agent_id": "bound-agent"})
	filtered, err := retrieveH(metaCtx, RetrieveInput{Query: "qvzxm widget", Limit: 10})
	if err != nil {
		t.Fatalf("retrieve (with meta): %v", err)
	}
	if filtered.Structured.DegradedAgentFilter {
		t.Error("expected DegradedAgentFilter=false on a clean bound-agent read")
	}
	foundAuth, foundDeploy := false, false
	for _, it := range filtered.Structured.Items {
		if it.Content == "auth widget note qvzxm" {
			foundAuth = true
		}
		if it.Content == "deploy widget note qvzxm" {
			foundDeploy = true
		}
	}
	if !foundAuth {
		t.Error("expected the allow-topic memory in the bound-agent's result")
	}
	if foundDeploy {
		t.Error("the agent filter must have subtracted the non-allow-topic memory")
	}

	// A non-string agent_id value in _meta must be nil-safe (type-assert fails
	// silently, scope.Agent stays empty).
	badMetaCtx := server.WithRequestMeta(ctx, map[string]any{"agent_id": 12345})
	badMetaResp, err := retrieveH(badMetaCtx, RetrieveInput{Query: "qvzxm widget", Limit: 10})
	if err != nil {
		t.Fatalf("retrieve (bad meta type): %v", err)
	}
	if len(badMetaResp.Structured.Items) < 2 {
		t.Errorf("non-string agent_id must be inert (nil-safe), got %d items", len(badMetaResp.Structured.Items))
	}
}
