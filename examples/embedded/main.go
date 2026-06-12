// Command embedded demonstrates the Stowage embedded mode (NewEmbedded).
//
// It boots an in-process Stowage stack (sqlite + mock gateway), ingests a
// short three-turn conversation, waits for the pipeline to settle, retrieves
// relevant memories, and prints the citation handles it receives.
//
// This binary is intentionally CGo-free (built with CGO_ENABLED=0) as proof
// of the Wails desktop embedding posture (D-004, D-022).
//
// Usage:
//
//	CGO_ENABLED=0 go run ./examples/embedded
//	CGO_ENABLED=0 go build -o /tmp/stowage-embedded ./examples/embedded
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/hurtener/stowage/internal/config"
	stowage "github.com/hurtener/stowage/sdk/stowage"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "embedded: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	// ── Temp database (CGo-free sqlite driver — D-004) ────────────────────
	dir, err := os.MkdirTemp("", "stowage-embedded-*")
	if err != nil {
		return fmt.Errorf("mktemp: %w", err)
	}
	defer os.RemoveAll(dir) //nolint:errcheck

	dbPath := dir + "/stowage.db"

	cfg := config.Config{
		Store:   config.StoreConfig{Driver: "sqlite", DSN: dbPath},
		Gateway: config.GatewayConfig{Driver: "mock"},
	}

	// ── Boot in-process stack ─────────────────────────────────────────────
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client, closer, err := stowage.NewEmbedded(ctx, cfg, stowage.WithTenantID("example-tenant"))
	if err != nil {
		return fmt.Errorf("NewEmbedded: %w", err)
	}
	defer func() {
		shutCtx, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		if closeErr := closer(shutCtx); closeErr != nil {
			fmt.Fprintf(os.Stderr, "embedded: close: %v\n", closeErr)
		}
	}()

	// ── Ingest a short conversation ───────────────────────────────────────
	session := "example-session-001"
	ingestResp, err := client.Ingest(ctx, stowage.IngestRequest{
		Records: []stowage.RecordInput{
			{
				Role:      "user",
				Content:   "My flight to Tokyo is on the 15th of July. I need a window seat.",
				SessionID: session,
			},
			{
				Role:      "assistant",
				Content:   "Got it — I will remember your Tokyo flight date and your preference for window seats.",
				SessionID: session,
			},
			{
				Role:      "user",
				Content:   "Also, I am vegetarian. Please keep that in mind for any restaurant or meal suggestions.",
				SessionID: session,
			},
		},
	})
	if err != nil {
		return fmt.Errorf("ingest: %w", err)
	}
	fmt.Printf("ingested %d record(s): %v\n", len(ingestResp.IDs), ingestResp.IDs)

	// ── Give the async pipeline a moment to process ───────────────────────
	// The pipeline is fire-and-forget (D-025 P2). In embedded mode with the
	// mock gateway, extraction happens synchronously on flush. A brief wait is
	// sufficient for the smoke / example use case; production callers would
	// rely on the pipeline's own flush triggers instead.
	time.Sleep(200 * time.Millisecond)

	// ── Retrieve ──────────────────────────────────────────────────────────
	retrieveResp, err := client.Retrieve(ctx, stowage.RetrieveRequest{
		Query:      "Tokyo trip and dietary preferences",
		Limit:      5,
		SessionID:  session,
		ResponseID: "example-response-001",
	})
	if err != nil {
		return fmt.Errorf("retrieve: %w", err)
	}

	fmt.Printf("retrieve: response_id=%s items=%d degraded=%v\n",
		retrieveResp.ResponseID, len(retrieveResp.Items), retrieveResp.Degraded)

	// ── Print citations ───────────────────────────────────────────────────
	if len(retrieveResp.Items) == 0 {
		fmt.Println("no memories yet (pipeline still processing or degraded mode — OK)")
	} else {
		fmt.Printf("citations:\n")
		for i, item := range retrieveResp.Items {
			fmt.Printf("  [%d] kind=%-12s score=%.3f citation=%s\n",
				i+1, item.Kind, item.Score, item.Citation)
			fmt.Printf("       %s\n", truncate(item.Content, 80))
		}
	}

	fmt.Println("done.")
	return nil
}

// truncate returns s truncated to at most n runes, with "…" appended if cut.
func truncate(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n-1]) + "…"
}
