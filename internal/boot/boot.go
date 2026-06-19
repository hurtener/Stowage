// Package boot assembles the Stowage core subsystem stack from a validated
// *config.Config. It is the single canonical boot sequence shared by
// cmd/stowage (serve and mcp subcommands) and sdk/stowage (NewEmbedded).
//
// Responsibilities:
//   - telemetry (logger + Prometheus registry)
//   - store open + migrate
//   - gateway open + probe (failure = degraded warn, not fatal — D-036)
//   - vindex open
//   - reconcile.Embedder create + Start
//   - retrieval.Retriever create (injection-recording, rerank, grants)
//   - topics.Service create
//   - grants.Service create
//
// Open builds the static stack only. Turning it into a live system — the
// pipeline buffer/extract/reconcile stages, the lifecycle sweeps, and the
// embedding BackfillSweep — is the job of StartPipeline (see pipeline.go), the
// single canonical post-boot wiring seam shared by `stowage serve`,
// `stowage mcp`, and `sdk/stowage` (D-068). Open deliberately does NOT do:
//   - HTTP listening (cmd/stowage serve)
//   - MCP transport (cmd/stowage mcp)
//   - the live derivation system — pipeline stages, lifecycle sweeps, and the
//     embedding BackfillSweep (all three: use boot.StartPipeline)
//   - slog.SetDefault (callers decide whether to replace the global logger)
//
// Usage:
//
//	stack, err := boot.Open(ctx, cfg)
//	if err != nil { ... }
//	defer stack.Close(shutdownCtx)
//	slog.SetDefault(stack.Log)              // optional: replace global logger
//	p, err := boot.StartPipeline(ctx, stack, cfg) // start the live system
//	defer p.Drain(shutdownCtx)
package boot

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"time"

	"log/slog"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/hurtener/stowage/internal/config"
	"github.com/hurtener/stowage/internal/gateway"
	"github.com/hurtener/stowage/internal/grants"
	"github.com/hurtener/stowage/internal/reconcile"
	"github.com/hurtener/stowage/internal/retrieval"
	"github.com/hurtener/stowage/internal/store"
	"github.com/hurtener/stowage/internal/telemetry"
	"github.com/hurtener/stowage/internal/topics"
	"github.com/hurtener/stowage/internal/traces"
	"github.com/hurtener/stowage/internal/vindex"
)

// Stack is the assembled core set of Stowage subsystems. All fields are
// non-nil after a successful Open. The caller owns the Stack and MUST call
// Close to release resources.
//
// Concurrent reuse: the Stack struct is immutable after Open; the composed
// subsystems carry their own concurrency guarantees (D-025 discipline).
type Stack struct {
	// Observability.
	Log     *slog.Logger
	Metrics *prometheus.Registry

	// Subsystems.
	Store     store.Store
	Gateway   gateway.Gateway
	VIndex    vindex.Index
	Embedder  *reconcile.Embedder
	Retriever *retrieval.Retriever
	TopicSvc  *topics.Service
	GrantsSvc *grants.Service

	// TraceSigner is the ed25519 key for signing reasoning-trace exports (Phase 26,
	// D-086). nil when trace.signing_key is unset (bundles returned unsigned).
	TraceSigner ed25519.PrivateKey

	closers []func(context.Context) error
}

// Open assembles the core stack from a validated *config.Config. ctx governs
// the lifetime of background goroutines started during Open (the embedder's
// processing goroutine). The caller must call stack.Close when done; Close
// does not cancel ctx — the caller is responsible for the context lifecycle.
//
// On error, any already-opened subsystems are closed before returning so the
// caller never has a partially-open stack to manage.
func Open(ctx context.Context, cfg *config.Config) (*Stack, error) {
	if cfg == nil {
		return nil, fmt.Errorf("boot: cfg is required")
	}

	s := &Stack{}

	// 1. Telemetry — logger + Prometheus registry.
	var err error
	s.Log, s.Metrics, err = telemetry.New(telemetry.Config{
		LogLevel:  cfg.Telemetry.LogLevel,
		LogFormat: cfg.Telemetry.LogFormat,
	})
	if err != nil {
		return nil, fmt.Errorf("boot: telemetry: %w", err)
	}

	// 2. Store — open driver + apply pending migrations (idempotent).
	s.Store, err = store.Open(ctx, cfg.Store)
	if err != nil {
		return nil, fmt.Errorf("boot: store: %w", err)
	}
	s.closers = append(s.closers, func(ctx context.Context) error {
		closeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		return s.Store.Close(closeCtx)
	})

	if err := s.Store.Migrate(ctx); err != nil {
		_ = s.close(ctx)
		return nil, fmt.Errorf("boot: migrate: %w", err)
	}

	// 3. Gateway — open intelligence seam. A probe failure is a degraded
	// warning (D-036), never a fatal boot error.
	s.Gateway, err = gateway.Open(ctx, cfg.Gateway, s.Log, s.Metrics)
	if err != nil {
		_ = s.close(ctx)
		return nil, fmt.Errorf("boot: gateway: %w", err)
	}
	s.closers = append(s.closers, s.Gateway.Close)

	// Wire gateway-call usage events onto the meter (§8/§10): every provider call is
	// emitted to the event stream for cost governance + audit, scoped to the caller.
	wireGatewayUsageEvents(s.Gateway, s.Store.Events(), s.Log)

	if probeErr := s.Gateway.Probe(ctx); probeErr != nil {
		s.Log.Warn("boot: gateway probe failed (degraded mode — vector lane disabled until provider recovers)",
			"err", probeErr)
	}

	// 4. VIndex — open vector index driver.
	s.VIndex, err = vindex.Open(cfg.VIndex, s.Store.Vectors(), cfg.Gateway.EmbedDims, cfg.Gateway.EmbedModel)
	if err != nil {
		_ = s.close(ctx)
		return nil, fmt.Errorf("boot: vindex: %w", err)
	}

	// 4a. Reindex guard (§10 gateway-seam rule): a model change is an EXPLICIT reindex,
	// never a silent mix of incompatible embeddings. If any persisted vector was
	// written with an embedding model other than the configured one, fail loud — the
	// operator must reindex (re-embed) under the new model before serving.
	if models, merr := s.Store.Vectors().DistinctModels(ctx); merr != nil {
		s.Log.Warn("boot: could not check persisted embedding model — skipping reindex guard", "err", merr)
	} else if gerr := checkEmbedModel(models, cfg.Gateway.EmbedModel); gerr != nil {
		_ = s.close(ctx)
		return nil, fmt.Errorf("boot: %w", gerr)
	}

	// 5. Embedder — create and start the background embedding goroutine.
	// ctx controls the embedder goroutine lifetime; callers control when
	// to cancel it (SIGTERM handler in cmd, user-cancel in SDK).
	s.Embedder = reconcile.NewEmbedder(s.Store.Vectors(), s.VIndex, s.Gateway, s.Log)
	s.Embedder.Start(ctx)

	// 6. Retriever — four-lane fusion + injection recording + rerank + grants.
	s.Retriever = retrieval.NewWithInjections( //nolint:contextcheck // writer goroutine owns its lifecycle ctx (Phase 11 pattern)
		s.Store.Memories(), s.Store.Records(), s.VIndex, s.Gateway,
		s.Store.Injections(), s.Log,
	)
	s.Retriever.WithRerankModel(cfg.Gateway.RerankModel)
	s.Retriever.WithEventCapture(s.Store.Events()) // Phase 26: async retrieve.query trace capture
	s.Retriever.SetGrants(s.Store.Grants())
	s.closers = append(s.closers, func(context.Context) error {
		s.Retriever.Close() // drains injection writer goroutine
		return nil
	})

	// 7. Topics service — extraction magnet + virtual pack logic.
	s.TopicSvc = topics.New(s.Store.Topics(), s.Log, cfg.Profile)

	// 8. Grants service — group/grant management and zone-ceiling enforcement.
	s.GrantsSvc = grants.New(s.Store.Grants(), s.Store.Events(), s.Log)

	// 9. Trace signing key (Phase 26, D-086). Optional: empty ⇒ unsigned exports.
	// When set, the config holds an env.VAR ref to a base64 ed25519 seed (D-030);
	// resolve + parse fail-loud (Validate already enforced the env. prefix).
	if cfg.Trace.SigningKey != "" {
		seed, rerr := config.ResolveEnvRef(cfg.Trace.SigningKey)
		if rerr != nil {
			return nil, fmt.Errorf("boot: trace.signing_key: %w", rerr)
		}
		signer, perr := traces.ParseSigningKey(seed)
		if perr != nil {
			return nil, fmt.Errorf("boot: trace.signing_key: %w", perr)
		}
		s.TraceSigner = signer
	}

	return s, nil
}

// Close releases all stack subsystems in reverse dependency order. Safe to
// call on a partially-open stack (returned alongside an Open error). Joins
// all closer errors.
func (s *Stack) Close(ctx context.Context) error {
	return s.close(ctx)
}

// close is the internal implementation of Close, called from both Close and
// the error-return paths inside Open.
func (s *Stack) close(ctx context.Context) error {
	var errs []error
	for i := len(s.closers) - 1; i >= 0; i-- {
		if err := s.closers[i](ctx); err != nil {
			errs = append(errs, err)
		}
	}
	s.closers = nil
	return errors.Join(errs...)
}
