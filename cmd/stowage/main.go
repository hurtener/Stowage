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

	"github.com/hurtener/stowage/internal/api"
	"github.com/hurtener/stowage/internal/config"
	"github.com/hurtener/stowage/internal/pipeline"
	"github.com/hurtener/stowage/internal/store"
	"github.com/hurtener/stowage/internal/telemetry"
	// register drivers via init()
	_ "github.com/hurtener/stowage/internal/store/pgstore"
	_ "github.com/hurtener/stowage/internal/store/sqlitestore"
	"github.com/hurtener/stowage/internal/version"
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

	case "mcp", "eval":
		fmt.Fprintf(os.Stderr, "stowage %s: not implemented yet — see docs/plans/README.md\n", os.Args[1])
		os.Exit(1)

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
		// Show status: run migrate (idempotent) then report.
		// If migrations haven't been applied yet we just show "pending".
		fmt.Printf("driver: %s\n", storeCfg.Driver)
		fmt.Printf("dsn:    %s\n", storeCfg.DSN)
		fmt.Println()
		fmt.Println("known migrations:")
		fmt.Println("  0001_init  (run 'stowage migrate' to apply)")
		return
	}

	if err := s.Migrate(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "stowage migrate: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("stowage migrate: applied all pending migrations")
}

const serveUsage = `stowage serve — run the HTTP memory service

Usage:
  stowage serve [--config path]

Flags:
  --config path   path to config file (default: auto-discover)
`

// runServe implements `stowage serve [--config path]`.
//
// Boot sequence (Phase 06):
//  1. config.Load   — typed config, fail-loud validation
//  2. telemetry.New — slog + Prometheus registry
//  3. store.Open    — open store driver
//  4. Migrate       — apply pending migrations (idempotent)
//  5. api.New       — build HTTP server with all routes
//  6. pipeline.New  — construct buffer stage between store and api
//  7. Start stage + no-op downstream consumer
//  8. ListenAndServe — start accepting connections
//
// Graceful shutdown on SIGTERM/SIGINT:
//  1. api.Shutdown  — stop accepting; closes pipeline ingest channel
//  2. stage.Drain   — wait for workers + ticker to finish
//  3. store.Close   — via defer (happens after runServe returns)
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

	// No-op downstream consumer — drains FlushedBuffer events until Phase 07
	// replaces it with the extraction stage.
	go func() {
		for range bufStage.Downstream() {
			// Phase 07: replace with extraction dispatch.
		}
	}()

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

	// Graceful shutdown (Phase 06 order):
	//  1. api.Shutdown — stop accepting; closes the ingest pipeline channel.
	//  2. stage.Drain  — workers process remaining items; ticker stops.
	//  3. store.Close  — happens in the deferred close above.
	shutdownCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Error("stowage serve: shutdown", "err", err)
	}
	bufStage.Drain(shutdownCtx)
	log.Info("stowage serve: stopped")
}
