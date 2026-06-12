// Command stowage is the Stowage memory server: an HTTP service, an MCP
// server, and an operations CLI in one CGo-free static binary (RFC §2).
//
// Subcommands land with their phases (docs/plans/README.md); until then they
// report their status and exit non-zero so smoke scripts can assert on them.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/hurtener/stowage/eval/datasets/locomo"
	"github.com/hurtener/stowage/eval/datasets/longmemeval"
	"github.com/hurtener/stowage/internal/api"
	"github.com/hurtener/stowage/internal/config"
	"github.com/hurtener/stowage/internal/gateway"
	"github.com/hurtener/stowage/internal/grants"
	"github.com/hurtener/stowage/internal/lifecycle"
	"github.com/hurtener/stowage/internal/pipeline"
	"github.com/hurtener/stowage/internal/reconcile"
	"github.com/hurtener/stowage/internal/retrieval"
	"github.com/hurtener/stowage/internal/store"
	"github.com/hurtener/stowage/internal/store/migrations"
	"github.com/hurtener/stowage/internal/telemetry"
	"github.com/hurtener/stowage/internal/topics"
	"github.com/hurtener/stowage/internal/vindex"
	// register drivers via init()
	_ "github.com/hurtener/stowage/internal/gateway/bifrost" // SDK driver: all providers in-process (D-049)
	_ "github.com/hurtener/stowage/internal/gateway/mock"
	_ "github.com/hurtener/stowage/internal/gateway/openaicompat" // OpenAI-compatible HTTP client (D-040)
	_ "github.com/hurtener/stowage/internal/store/pgstore"
	_ "github.com/hurtener/stowage/internal/store/sqlitestore"
	"github.com/hurtener/stowage/internal/version"
	_ "github.com/hurtener/stowage/internal/vindex/hnsw" // register "hnsw" vindex driver (D-048)
)

const usage = `stowage — memory infrastructure for agentic systems

Usage:
  stowage <command> [flags]

Commands:
  config    configuration utilities (explain)
  serve     run the HTTP memory service        (lands in Phase 05)
  mcp       run the MCP tool server            (lands in Phase 17)
  migrate   apply store schema migrations
  eval      run the evaluation harness         (lands in Phase 13)
  version   print the build version
`

const configUsage = `stowage config — configuration utilities

Usage:
  stowage config <subcommand> [flags]

Subcommands:
  explain [--config path]   print effective config with provenance
`

const migrateUsage = `stowage migrate — apply store schema migrations

Usage:
  stowage migrate [--config path] [--dsn dsn] [--status]

Flags:
  --config path   path to config file (default: auto-discover)
  --dsn dsn       database DSN, overrides config
  --status        print applied/pending migrations and exit
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	switch os.Args[1] {
	case "version":
		fmt.Println(version.Version)

	case "config":
		runConfig(os.Args[2:])

	case "migrate":
		runMigrate(os.Args[2:])

	case "serve":
		runServe(os.Args[2:])

	case "mcp":
		fmt.Fprintf(os.Stderr, "stowage mcp: not implemented yet — lands in Phase 17\n")
		os.Exit(1)

	case "eval":
		runEval(os.Args[2:])

	default:
		fmt.Fprintf(os.Stderr, "stowage: unknown command %q\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}
}

// runConfig dispatches config subcommands.
// An unknown sub-subcommand exits 2 (AC — unknown sub-subcommand exits 2).
func runConfig(args []string) {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, configUsage)
		os.Exit(2)
	}
	switch args[0] {
	case "explain":
		runConfigExplain(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "stowage config: unknown subcommand %q\n\n%s", args[0], configUsage)
		os.Exit(2)
	}
}

// runConfigExplain implements `stowage config explain [--config path]`.
func runConfigExplain(args []string) {
	var configPath string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--config":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "stowage config explain: --config requires a path argument")
				os.Exit(2)
			}
			configPath = args[i+1]
			i++
		default:
			fmt.Fprintf(os.Stderr, "stowage config explain: unknown flag %q\n", args[i])
			os.Exit(2)
		}
	}

	cfg, err := config.Load(context.Background(), configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "stowage config explain: %v\n", err)
		os.Exit(1)
	}
	if err := cfg.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "stowage config explain: %v\n", err)
		os.Exit(1)
	}
	if err := cfg.Explain(os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "stowage config explain: %v\n", err)
		os.Exit(1)
	}
}

// runMigrate implements `stowage migrate [--config path] [--dsn dsn] [--status]`.
func runMigrate(args []string) {
	var (
		configPath  string
		dsnOverride string
		statusOnly  bool
	)
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--config":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "stowage migrate: --config requires a path argument")
				os.Exit(2)
			}
			configPath = args[i+1]
			i++
		case "--dsn":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "stowage migrate: --dsn requires an argument")
				os.Exit(2)
			}
			dsnOverride = args[i+1]
			i++
		case "--status":
			statusOnly = true
		case "--help", "-h":
			_, _ = fmt.Fprint(os.Stdout, migrateUsage)
			os.Exit(0)
		default:
			fmt.Fprintf(os.Stderr, "stowage migrate: unknown flag %q\n\n%s", args[i], migrateUsage)
			os.Exit(2)
		}
	}

	ctx := context.Background()

	cfg, err := config.Load(ctx, configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "stowage migrate: load config: %v\n", err)
		os.Exit(1)
	}

	storeCfg := cfg.Store
	if dsnOverride != "" {
		storeCfg.DSN = dsnOverride
	}

	s, err := store.Open(ctx, storeCfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "stowage migrate: open store: %v\n", err)
		os.Exit(1)
	}
	defer func() {
		if closeErr := s.Close(ctx); closeErr != nil {
			fmt.Fprintf(os.Stderr, "stowage migrate: close store: %v\n", closeErr)
		}
	}()

	if statusOnly {
		fmt.Printf("driver: %s\n", storeCfg.Driver)
		fmt.Printf("dsn:    %s\n", storeCfg.DSN)
		fmt.Println()
		fmt.Println("known migrations:")
		applied, aerr := s.AppliedMigrations(ctx)
		appliedSet := map[string]bool{}
		if aerr == nil {
			for _, v := range applied {
				appliedSet[v] = true
			}
		}
		for _, name := range migrations.Known(storeCfg.Driver) {
			status := "pending (run 'stowage migrate' to apply)"
			if appliedSet[name] {
				status = "applied"
			}
			fmt.Printf("  %-22s %s\n", name, status)
		}
		return
	}

	if err := s.Migrate(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "stowage migrate: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("stowage migrate: applied all pending migrations")
}

const evalUsage = `stowage eval — evaluation harness for the Stowage memory pipeline (Phase 13)

Usage:
  stowage eval <subcommand> [flags]

Subcommands:
  fetch --dataset <name>   download a dataset into eval/data/
                           known datasets: longmemeval, locomo
  ci                       print instructions for running the CI eval gate
`

const evalFetchUsage = `stowage eval fetch — download an eval dataset into eval/data/

Usage:
  stowage eval fetch --dataset <name> [--data-dir path]

Flags:
  --dataset name    dataset to fetch (longmemeval | locomo)
  --data-dir path   root directory for downloaded data (default: eval/data)
`

// runEval dispatches eval subcommands (Phase 13).
func runEval(args []string) {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, evalUsage)
		os.Exit(2)
	}
	switch args[0] {
	case "fetch":
		runEvalFetch(args[1:])
	case "ci":
		fmt.Println("Run the CI eval gate with:")
		fmt.Println("  make eval-ci")
		fmt.Println("or:")
		fmt.Println("  CGO_ENABLED=1 go test -race -v -timeout=5m -run 'TestEvalCI|TestEvalCIGateBites' ./eval/harness/")
	case "--help", "-h", "help":
		_, _ = fmt.Fprint(os.Stdout, evalUsage)
	default:
		fmt.Fprintf(os.Stderr, "stowage eval: unknown subcommand %q\n\n%s", args[0], evalUsage)
		os.Exit(2)
	}
}

// runEvalFetch implements `stowage eval fetch --dataset <name>`.
func runEvalFetch(args []string) {
	var (
		dataset string
		dataDir string
	)
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--dataset":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "stowage eval fetch: --dataset requires a name argument")
				os.Exit(2)
			}
			dataset = args[i+1]
			i++
		case "--data-dir":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "stowage eval fetch: --data-dir requires a path argument")
				os.Exit(2)
			}
			dataDir = args[i+1]
			i++
		case "--help", "-h":
			_, _ = fmt.Fprint(os.Stdout, evalFetchUsage)
			os.Exit(0)
		default:
			fmt.Fprintf(os.Stderr, "stowage eval fetch: unknown flag %q\n\n%s", args[i], evalFetchUsage)
			os.Exit(2)
		}
	}

	if dataset == "" {
		fmt.Fprintln(os.Stderr, "stowage eval fetch: --dataset is required")
		os.Exit(2)
	}
	if dataDir == "" {
		dataDir = "eval/data"
	}

	ctx := context.Background()
	switch dataset {
	case "longmemeval":
		dest, err := longmemeval.Fetch(ctx, dataDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "stowage eval fetch: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("stowage eval fetch: longmemeval saved to %s\n", dest)
	case "locomo":
		dest, err := locomo.Fetch(ctx, dataDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "stowage eval fetch: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("stowage eval fetch: locomo saved to %s\n", dest)
	default:
		fmt.Fprintf(os.Stderr, "stowage eval fetch: unknown dataset %q (known: longmemeval, locomo)\n", dataset)
		os.Exit(2)
	}
}

const serveUsage = `stowage serve — run the HTTP memory service

Usage:
  stowage serve [--config path]

Flags:
  --config path   path to config file (default: auto-discover)
`

// runServe implements `stowage serve [--config path]`.
//
// Boot sequence (Phase 07):
//  1. config.Load      — typed config, fail-loud validation
//  2. telemetry.New    — slog + Prometheus registry
//  3. store.Open       — open store driver
//  4. Migrate          — apply pending migrations (idempotent)
//  5. gateway.Open     — open intelligence gateway (mock default; D-036)
//  6. gateway.Probe    — probe; failure = warn + degraded (D-036), never fatal
//  7. api.New          — build HTTP server with all routes
//  8. pipeline.New     — construct buffer stage between store and api
//  9. topics.New       — construct topics service
//
// 10. pipeline.NewExtractStage — extraction stage wired to buffer downstream
// 11. Start stages + no-op Phase 08 placeholder consumer
// 12. lifecycle.Manager.Start  — decay/dedupe/rollup/re-enqueue sweeps (Phase 14)
// 13. ListenAndServe   — start accepting connections
//
// Graceful shutdown on SIGTERM/SIGINT (Phase 07 order):
//  1. api.Shutdown          — stop accepting; closes pipeline ingest channel
//  2. bufStage.Drain        — workers + ticker finish
//  3. extractStage.Drain    — extraction workers finish
//  4. lifecycle.Manager.Stop — sweep goroutines finish
//  5. gw.Close              — gateway flush + release
//  6. store.Close           — via defer (happens after runServe returns)
func runServe(args []string) {
	var configPath string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--config":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "stowage serve: --config requires a path argument")
				os.Exit(2)
			}
			configPath = args[i+1]
			i++
		case "--help", "-h":
			_, _ = fmt.Fprint(os.Stdout, serveUsage)
			os.Exit(0)
		default:
			fmt.Fprintf(os.Stderr, "stowage serve: unknown flag %q\n\n%s", args[i], serveUsage)
			os.Exit(2)
		}
	}

	ctx := context.Background()

	cfg, err := config.Load(ctx, configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "stowage serve: load config: %v\n", err)
		os.Exit(1)
	}
	if err := cfg.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "stowage serve: invalid config: %v\n", err)
		os.Exit(1)
	}

	log, reg, err := telemetry.New(telemetry.Config{
		LogLevel:  cfg.Telemetry.LogLevel,
		LogFormat: cfg.Telemetry.LogFormat,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "stowage serve: telemetry: %v\n", err)
		os.Exit(1)
	}
	slog.SetDefault(log)

	// Prometheus registry: register default metrics.
	_ = reg // passed to api.New below; also used for metrics endpoint

	st, err := store.Open(ctx, cfg.Store)
	if err != nil {
		log.Error("stowage serve: open store", "err", err)
		os.Exit(1)
	}
	// Ensure clean close on exit.
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if closeErr := st.Close(closeCtx); closeErr != nil {
			log.Error("stowage serve: close store", "err", closeErr)
		}
	}()

	// Auto-migrate (idempotent — safe to always run on boot).
	if err := st.Migrate(ctx); err != nil {
		log.Error("stowage serve: migrate", "err", err)
		os.Exit(1)
	}

	// Open the intelligence gateway (Phase 07). Default driver: "mock".
	// gateway.Open failure is a configuration error → fatal.
	// gateway.Probe failure is degraded (D-036) → warn + continue.
	gw, err := gateway.Open(ctx, cfg.Gateway, log, reg)
	if err != nil {
		log.Error("stowage serve: gateway open failed", "err", err)
		os.Exit(1)
	}
	if probeErr := gw.Probe(ctx); probeErr != nil {
		log.Warn("stowage serve: gateway probe failed (degraded mode — extraction will dead-letter until provider recovers)",
			"err", probeErr)
	}

	// Expose additional metrics.
	_ = prometheus.NewRegistry() // reg passed from telemetry

	srv, err := api.New(cfg, st, log, reg)
	if err != nil {
		log.Error("stowage serve: api.New", "err", err)
		os.Exit(1)
	}

	// Construct buffer stage (Phase 06). Stage sits between store and api.
	trig := pipeline.TriggersFromConfig(cfg.Profile)
	bufStage := pipeline.New(st, log, trig, srv.Pipeline())
	srv.SetStage(bufStage)
	bufStage.Start(ctx)

	// Topics service + extract stage (Phase 07).
	topicSvc := topics.New(st.Topics(), log, cfg.Profile)
	srv.SetTopicService(topicSvc)
	extractStage := pipeline.NewExtractStage(st, gw, topicSvc, log, cfg.Profile, bufStage.Downstream())
	extractStage.Start(ctx)

	// Phase 09: vindex, embedder, and retriever (D-046, D-047, D-048).
	// EmbedDims defaults to 0 when unconfigured; vindex skips dim checks then.
	// Driver selected by cfg.VIndex.Driver (default "hnsw" per D-048).
	vi, err := vindex.Open(cfg.VIndex, st.Vectors(), cfg.Gateway.EmbedDims, cfg.Gateway.EmbedModel)
	if err != nil {
		log.Error("stowage serve: vindex open failed", "err", err)
		os.Exit(1)
	}
	embedder := reconcile.NewEmbedder(st.Vectors(), vi, gw, log)
	embedder.Start(ctx)
	go embedder.BackfillSweep(ctx)

	// Phase 11: wire InjectionStore so every retrieve call records injection rows
	// (async, zero added latency — P2 fire-and-forget via InjectionWriter, D-051).
	retriever := retrieval.NewWithInjections(st.Memories(), st.Records(), vi, gw, st.Injections(), log)
	retriever.WithRerankModel(cfg.Gateway.RerankModel) // Phase 12: rerank lane (D-052)
	retriever.SetGrants(st.Grants())                   // Phase 15: grant-aware effective scopes (D-060)
	srv.SetRetriever(retriever)

	// Phase 15: grants service — group/grant management and zone-ceiling enforcement.
	grantsSvc := grants.New(st.Grants(), st.Events(), log)
	srv.SetGrantsService(grantsSvc)

	// Phase 08: reconciliation stage wired to extract stage downstream.
	reconcileStage := reconcile.New(
		st.Memories(),
		st.Ops(),
		st.Events(),
		gw,
		log,
		extractStage.Downstream(),
	)
	reconcileStage.SetEmbedder(embedder)
	reconcileStage.SetScopeInvalidator(retriever.Cache()) // Phase 12: cache invalidation on commit (D-053)
	reconcileStage.Start(ctx)

	// Phase 14: lifecycle sweeps (decay, dedupe, rollup, re-enqueue).
	// STOWAGE_SWEEP_FORCE=1 runs all sweeps once synchronously before serve (for smoke testing).
	lcMgr := lifecycle.New(st, log, lifecycle.DefaultProfile(), srv.PipelineIn())
	if os.Getenv("STOWAGE_SWEEP_FORCE") != "" {
		log.Info("stowage serve: STOWAGE_SWEEP_FORCE set — running all sweeps once")
		lcMgr.RunForce(ctx)
	}
	lcMgr.Start(ctx)

	// Start HTTP server in a goroutine.
	servErr := make(chan error, 1)
	go func() {
		if listenErr := srv.ListenAndServe(); listenErr != nil && !errors.Is(listenErr, http.ErrServerClosed) {
			servErr <- listenErr
		}
	}()

	log.Info("stowage serve: ready", "addr", cfg.Server.Listen)

	// Wait for termination signal or server error.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

	select {
	case sig := <-sigCh:
		log.Info("stowage serve: shutting down", "signal", sig)
	case err := <-servErr:
		log.Error("stowage serve: server error", "err", err)
		os.Exit(1)
	}

	// Graceful shutdown (Phase 07 order):
	//  1. api.Shutdown       — stop accepting; closes the ingest pipeline channel.
	//  2. bufStage.Drain     — buffer workers + ticker finish; closes FlushedBuffer ch.
	//  3. extractStage.Drain — extract workers finish; closes CandidateBatch ch.
	//  4. gw.Close           — gateway flush + release.
	//  5. store.Close        — happens in the deferred close above.
	shutdownCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Error("stowage serve: shutdown", "err", err)
	}
	bufStage.Drain(shutdownCtx)
	extractStage.Drain(shutdownCtx)
	reconcileStage.Drain(shutdownCtx)
	lcMgr.Stop()
	if gwErr := gw.Close(shutdownCtx); gwErr != nil {
		log.Warn("stowage serve: gateway close", "err", gwErr)
	}
	log.Info("stowage serve: stopped")
}
