package mcpserver

import (
	"context"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/hurtener/dockyard/runtime/server"
	"github.com/hurtener/stowage/internal/identity"
)

// TestRuntimeHarborCompletion runs the pinned REAL Harbor MCP driver, filtered
// planner view, executor and completion hook against this checkout's real
// Stowage MCP server and SQLite store. The child fixture lives in test/integration
// and is copied into the pinned Harbor checkout by the read-only CI workflow.
// No LLM credentials, user transcripts, production credentials or deployments.
func TestRuntimeHarborCompletion(t *testing.T) {
	checkout := os.Getenv("STOWAGE_HARBOR_CHECKOUT")
	if checkout == "" {
		t.Skip("STOWAGE_HARBOR_CHECKOUT not configured; run the runtime-hook integration workflow")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	git := exec.CommandContext(ctx, "git", "-C", checkout, "rev-parse", "HEAD")
	sha, err := git.Output()
	if err != nil || strings.TrimSpace(string(sha)) != "3f758afd07bafc1add74e60707a01fb833aa5d8f" {
		t.Fatalf("integration requires the reviewed Harbor v1.31.4 source: %s %v", sha, err)
	}
	svc := newHandlerServices(t)
	// Same tenant-scoped resolution as keyring mode; Harbor supplies the real
	// per-call user/session metadata. JWT issuer behavior is unchanged and is
	// covered by the existing authentication tests, not simulated here.
	svc.ScopeFn = StdioScopeFn("acme")
	srv, err := NewRuntime(server.Info{Name: "stowage-runtime-integration", Version: "1"}, svc)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := srv.HTTPHandler(nil)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(handler)
	defer ts.Close()
	cmd := exec.CommandContext(ctx, "go", "test", "-race", "-count=1", "-run", "^TestStowageRuntimeCompletion$", "./internal/runtime/assemble")
	cmd.Dir = checkout
	cmd.Env = append(os.Environ(), "STOWAGE_RUNTIME_TEST_URL="+ts.URL)
	output, err := cmd.CombinedOutput()
	t.Logf("pinned Harbor integration:\n%s", output)
	if err != nil {
		t.Fatalf("Harbor completion integration failed: %v", err)
	}
	for _, tc := range []struct {
		user    string
		session string
		goal    string
		count   int
	}{
		{"alice", "s-alice", "Remember Alice's project decision.", 2},
		{"bob", "s-bob", "Remember Bob's project decision.", 2},
		{"alice", "s-cancel", "Preserve a cancelled conversation.", 1},
		{"alice", "s-off", "Do not automatically capture this run.", 0},
		{"alice", "s-missing", "A missing sink must not fail the answer.", 0},
	} {
		scope := identity.Scope{Tenant: "acme", User: tc.user}
		recs, cursor, err := svc.Store.Records().ListBySession(ctx, scope, tc.session, "", 100, "")
		if err != nil {
			t.Fatal(err)
		}
		if cursor != "" || len(recs) != tc.count {
			t.Errorf("%s/%s: got %d durable records (cursor=%q); want %d", tc.user, tc.session, len(recs), cursor, tc.count)
		}
		foundGoal := false
		for _, rec := range recs {
			if rec.TenantID != "acme" || rec.UserID != tc.user || rec.SessionID != tc.session {
				t.Errorf("record crossed the run scope: %+v", rec)
			}
			foundGoal = foundGoal || (rec.Role == "user" && rec.Content == tc.goal)
		}
		if tc.count > 0 && !foundGoal {
			t.Errorf("%s: runtime-authored user goal was not stored verbatim", tc.session)
		}
	}
}
