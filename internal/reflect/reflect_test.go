package reflect

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/hurtener/stowage/internal/gateway"
	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/pipeline"
	"github.com/hurtener/stowage/internal/store"
)

// fakeGateway returns queued JSON for Complete and records requests.
type fakeGateway struct {
	responses []json.RawMessage
	idx       int
	reqs      []gateway.CompleteRequest
	err       error
}

func (f *fakeGateway) Complete(_ context.Context, req gateway.CompleteRequest) (gateway.CompleteResponse, error) {
	f.reqs = append(f.reqs, req)
	if f.err != nil {
		return gateway.CompleteResponse{}, f.err
	}
	out := json.RawMessage(`{"reflections":[]}`)
	if f.idx < len(f.responses) {
		out = f.responses[f.idx]
		f.idx++
	}
	return gateway.CompleteResponse{JSON: out}, nil
}
func (f *fakeGateway) Embed(context.Context, gateway.EmbedRequest) (gateway.EmbedResponse, error) {
	return gateway.EmbedResponse{}, nil
}
func (f *fakeGateway) Probe(context.Context) error { return nil }
func (f *fakeGateway) Close(context.Context) error { return nil }
func (f *fakeGateway) Rerank(context.Context, gateway.RerankRequest) (gateway.RerankResponse, error) {
	return gateway.RerankResponse{}, nil
}

func rec(id, sess, branch, outcome, content string, occurred int64) store.Record {
	return store.Record{ID: id, SessionID: sess, BranchID: branch, Outcome: outcome, Content: content, Role: "tool", OccurredAt: occurred}
}

func TestAssembleTrajectories(t *testing.T) {
	// Two sessions; s1 ends in failure (terminal = last tagged), s2 success.
	in := []store.Record{
		rec("a", "s1", "main", "success", "tried A", 1),
		rec("b", "s1", "main", "failure", "A broke", 2),
		rec("c", "s2", "main", "success", "B worked", 3),
	}
	got := AssembleTrajectories(in)
	if len(got) != 2 {
		t.Fatalf("expected 2 trajectories, got %d", len(got))
	}
	if got[0].SessionID != "s1" || got[0].Outcome != "failure" || len(got[0].Records) != 2 {
		t.Errorf("s1 trajectory wrong: %+v", got[0])
	}
	if got[1].SessionID != "s2" || got[1].Outcome != "success" {
		t.Errorf("s2 trajectory wrong: %+v", got[1])
	}
}

func TestAssembleTrajectories_DistinctUsersSameSession(t *testing.T) {
	// Two users sharing a session_id must NOT merge into one trajectory.
	a := store.Record{ID: "a", SessionID: "s1", BranchID: "main", UserID: "u1", Outcome: "success", Content: "x"}
	b := store.Record{ID: "b", SessionID: "s1", BranchID: "main", UserID: "u2", Outcome: "failure", Content: "y"}
	got := AssembleTrajectories([]store.Record{a, b})
	if len(got) != 2 {
		t.Fatalf("distinct users sharing a session must yield 2 trajectories, got %d", len(got))
	}
	if got[0].UserID == got[1].UserID {
		t.Error("trajectories not split by user")
	}
}

func TestTrajectoryKey_StableAndOutcomeSensitive(t *testing.T) {
	t1 := Trajectory{Outcome: "success", Records: []store.Record{rec("a", "s", "m", "success", "x", 1), rec("b", "s", "m", "success", "y", 2)}}
	t2 := Trajectory{Outcome: "success", Records: []store.Record{rec("b", "s", "m", "success", "y", 2), rec("a", "s", "m", "success", "x", 1)}}
	if t1.Key() != t2.Key() {
		t.Error("key should be order-independent")
	}
	t3 := Trajectory{Outcome: "failure", Records: t1.Records}
	if t1.Key() == t3.Key() {
		t.Error("key should change with outcome")
	}
}

func TestBuildReflectionPrompt_Golden(t *testing.T) {
	traj := Trajectory{Outcome: "failure", Records: []store.Record{
		rec("r1", "s1", "main", "", "ran the migration", 1),
		rec("r2", "s1", "main", "failure", "  it deadlocked  ", 2),
	}}
	sys, user := BuildReflectionPrompt(traj)
	if !strings.Contains(sys, "Reflector") || !strings.Contains(sys, "failure_mode") {
		t.Errorf("reflection system prompt missing ACE framing: %q", sys)
	}
	want := "Task outcome: FAILURE\n\nTrajectory:\n[record r1] role: tool\nran the migration\n[record r2] role: tool\nit deadlocked\n"
	if user != want {
		t.Errorf("reflection user prompt mismatch:\n got: %q\nwant: %q", user, want)
	}
}

func TestReflect_BuildsStampedCandidates(t *testing.T) {
	traj := Trajectory{Outcome: "success", SessionID: "s1", BranchID: "main", Records: []store.Record{
		rec("r1", "s1", "main", "success", "used a retry with backoff", 1),
	}}
	resp := `{"reflections":[{
		"kind":"strategy","content":"On transient errors, retry with exponential backoff.",
		"context":"","entities":["retry"],"keywords":["backoff"],
		"anticipated_queries":["how to handle transient errors"],
		"importance":4,"confidence":0.9,
		"provenance":[{"record_id":"r1","span_start":0,"span_end":10}]
	}]}`
	fg := &fakeGateway{responses: []json.RawMessage{json.RawMessage(resp)}}
	cands, err := Reflect(context.Background(), fg, identity.Scope{Tenant: "t"}, traj, "")
	if err != nil {
		t.Fatalf("Reflect: %v", err)
	}
	if len(cands) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(cands))
	}
	c := cands[0]
	if c.Kind != "strategy" || c.TrustSource != ReflectionTrustSource || c.Stability != reflectSeedStability {
		t.Errorf("candidate not stamped correctly: %+v", c)
	}
	if !pipeline.IsReflectionKind(c.Kind) {
		t.Error("produced a non-reflection kind")
	}
	// The Complete call must be schema-constrained (§10).
	if len(fg.reqs) != 1 || len(fg.reqs[0].Schema) == 0 {
		t.Error("reflection Complete call was not schema-constrained")
	}
}

// TestReflect_ModelWiring verifies the D-132 per-stage model knob reaches the
// reflection Complete call: a set model appears on the request, default leaves it empty.
func TestReflect_ModelWiring(t *testing.T) {
	traj := Trajectory{Outcome: "success", SessionID: "s1", BranchID: "main", Records: []store.Record{
		rec("r1", "s1", "main", "success", "used a retry with backoff", 1),
	}}

	for _, tc := range []struct{ model string }{{"inception/mercury-2"}, {""}} {
		fg := &fakeGateway{responses: []json.RawMessage{json.RawMessage(`{"reflections":[]}`)}}
		if _, err := Reflect(context.Background(), fg, identity.Scope{Tenant: "t"}, traj, tc.model); err != nil {
			t.Fatalf("Reflect(%q): %v", tc.model, err)
		}
		if len(fg.reqs) != 1 {
			t.Fatalf("model=%q: expected 1 Complete call, got %d", tc.model, len(fg.reqs))
		}
		if fg.reqs[0].Model != tc.model {
			t.Errorf("reflect req.Model = %q, want %q", fg.reqs[0].Model, tc.model)
		}
	}
}

func TestReflect_DropsProvenanceToUnknownRecords(t *testing.T) {
	traj := Trajectory{Outcome: "failure", Records: []store.Record{rec("r1", "s", "m", "failure", "x", 1)}}
	// Provenance references r9 (not in the trajectory) → candidate has no valid
	// provenance → dropped (P1).
	resp := `{"reflections":[{"kind":"failure_mode","content":"avoid X","context":"",
		"entities":[],"keywords":[],"anticipated_queries":["q"],"importance":3,"confidence":0.5,
		"provenance":[{"record_id":"r9","span_start":0,"span_end":1}]}]}`
	fg := &fakeGateway{responses: []json.RawMessage{json.RawMessage(resp)}}
	cands, err := Reflect(context.Background(), fg, identity.Scope{Tenant: "t"}, traj, "")
	if err != nil {
		t.Fatalf("Reflect: %v", err)
	}
	if len(cands) != 0 {
		t.Errorf("candidate with no valid provenance must be dropped (P1), got %d", len(cands))
	}
}

func TestReflect_EmptyTrajectoryNoCall(t *testing.T) {
	fg := &fakeGateway{}
	cands, err := Reflect(context.Background(), fg, identity.Scope{Tenant: "t"}, Trajectory{}, "")
	if err != nil || len(cands) != 0 {
		t.Errorf("empty trajectory: expected (nil,0), got (%v,%d)", err, len(cands))
	}
	if len(fg.reqs) != 0 {
		t.Error("empty trajectory should not call the gateway")
	}
}
