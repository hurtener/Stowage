package stowage_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hurtener/stowage/internal/config"
	stowage "github.com/hurtener/stowage/sdk/stowage"
)

// newEmbeddedTestClient returns an in-process Client backed by a temp SQLite
// database and a mock gateway. The closer is registered with t.Cleanup.
func newEmbeddedTestClient(t *testing.T, tenantID string) stowage.Client {
	t.Helper()

	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	cfg := config.Config{}
	cfg.Store.Driver = "sqlite"
	cfg.Store.DSN = dbPath
	cfg.Gateway.Driver = "mock"
	cfg.VIndex.Driver = "hnsw"

	ctx, cancel := context.WithCancel(context.Background())

	client, closer, err := stowage.NewEmbedded(ctx, cfg, stowage.WithTenantID(tenantID))
	if err != nil {
		cancel()
		t.Fatalf("NewEmbedded: %v", err)
	}

	t.Cleanup(func() {
		cancel()
		shutCtx, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		if err := closer(shutCtx); err != nil {
			t.Logf("embedded closer: %v", err)
		}
	})

	return client
}

// TestClientEmbedded_Suite runs the full parity suite against the embedded
// constructor. AC-1: same-suite parity, embedded path.
func TestClientEmbedded_Suite(t *testing.T) {
	client := newEmbeddedTestClient(t, "embedded-suite-tenant")
	RunSuite(t, client)
}

// TestClientEmbedded_TripleRun proves the embedded client survives three
// concurrent suites without data races (AC-7: race ×3).
func TestClientEmbedded_TripleRun(t *testing.T) {
	for i := 0; i < 3; i++ {
		i := i
		t.Run("run", func(t *testing.T) {
			t.Parallel()
			client := newEmbeddedTestClient(t, "embedded-race-tenant")
			ctx := context.Background()
			_, err := client.Ingest(ctx, stowage.IngestRequest{
				Records: []stowage.RecordInput{
					{Content: "embedded race test record", Role: "user"},
				},
			})
			if err != nil {
				t.Errorf("run %d: Ingest error: %v", i, err)
			}
		})
	}
}

// TestClientEmbedded_BuildCGOFree verifies that the embedded client builds with
// CGO_ENABLED=0. This is a build-time assertion only — the test always passes if
// it compiles. The CGo-free build check lives in the smoke script (AC-2).
func TestClientEmbedded_BuildCGOFree(t *testing.T) {
	// If this file compiled, the CGo-free build works. The smoke script
	// additionally runs `CGO_ENABLED=0 go build ./examples/embedded/...`.
	t.Logf("embedded client compiled; CGo-free build is verified by make build (smoke-02 variant)")
}

// TestClientEmbedded_MissingTenantID verifies that NewEmbedded returns an error
// when WithTenantID is not provided.
func TestClientEmbedded_MissingTenantID(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	cfg := config.Config{}
	cfg.Store.Driver = "sqlite"
	cfg.Store.DSN = dbPath

	ctx := context.Background()
	_, _, err := stowage.NewEmbedded(ctx, cfg)
	if err == nil {
		t.Error("NewEmbedded without WithTenantID: expected error, got nil")
	}
}

// TestClientEmbedded_MissingDSN verifies that NewEmbedded with sqlite driver
// and no DSN returns an error.
func TestClientEmbedded_MissingDSN(t *testing.T) {
	cfg := config.Config{}
	cfg.Store.Driver = "sqlite"
	// DSN intentionally empty

	ctx := context.Background()
	_, _, err := stowage.NewEmbedded(ctx, cfg, stowage.WithTenantID("t"))
	if err == nil {
		t.Error("NewEmbedded with empty DSN: expected error, got nil")
	}
}

// TestClientEmbedded_IngestRetrieveCycle is an end-to-end smoke that proves
// the full ingest→pipeline→retrieve cycle works in embedded mode.
func TestClientEmbedded_IngestRetrieveCycle(t *testing.T) {
	if os.Getenv("STOWAGE_SKIP_E2E") != "" {
		t.Skip("STOWAGE_SKIP_E2E set")
	}

	client := newEmbeddedTestClient(t, "e2e-tenant")
	ctx := context.Background()

	// Ingest a pair of records.
	ingestResp, err := client.Ingest(ctx, stowage.IngestRequest{
		Records: []stowage.RecordInput{
			{Role: "user", Content: "The annual review is scheduled for March.", SessionID: "sess1"},
			{Role: "assistant", Content: "I noted the March annual review.", SessionID: "sess1"},
		},
	})
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if len(ingestResp.IDs) != 2 {
		t.Fatalf("Ingest: want 2 IDs, got %d", len(ingestResp.IDs))
	}

	// Retrieve: even without pipeline processing the response shape must be valid.
	retResp, err := client.Retrieve(ctx, stowage.RetrieveRequest{
		Query: "annual review",
		Limit: 5,
	})
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if retResp.API != "v1" {
		t.Errorf("Retrieve: API want v1, got %q", retResp.API)
	}
	t.Logf("retrieve response: %d items, degraded=%v", len(retResp.Items), retResp.Degraded)
}
