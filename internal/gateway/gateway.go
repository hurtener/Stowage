// Package gateway defines the single intelligence seam for Stowage
// (RFC §7, P5, D-005, D-040).
//
// No provider wire formats appear here — they live exclusively in driver
// sub-packages (bifrost, mock). Callers obtain a Gateway by calling Open,
// which dispatches to the registered driver factory.
package gateway

import "context"

// Gateway is the intelligence seam (RFC §7, P5). All embedding and completion
// calls from the rest of the application flow through this interface; no
// caller ever imports a driver package directly (CLAUDE.md §13, §10).
type Gateway interface {
	// Embed returns float32 vectors for the given inputs. Model and dims are
	// pinned from config at construction time; callers do not specify them.
	Embed(ctx context.Context, req EmbedRequest) (EmbedResponse, error)

	// Complete performs a JSON-schema-constrained chat completion. Schema is
	// REQUIRED — free-text completions are forbidden (CLAUDE.md §10, D-040).
	Complete(ctx context.Context, req CompleteRequest) (CompleteResponse, error)

	// Probe validates that the provider is reachable and that the configured
	// model and dims match. Called once at boot; fails closed on mismatch.
	Probe(ctx context.Context) error

	// Close flushes pending batches and releases all resources.
	Close(ctx context.Context) error
}
