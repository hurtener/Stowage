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
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/hurtener/dockyard/runtime/server"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/hurtener/stowage/eval/datasets/locomo"
	"github.com/hurtener/stowage/eval/datasets/longmemeval"
	"github.com/hurtener/stowage/internal/api"
	"github.com/hurtener/stowage/internal/boot"
	"github.com/hurtener/stowage/internal/config"
	"github.com/hurtener/stowage/internal/mcpserver"
	"github.com/hurtener/stowage/internal/store"
	"github.com/hurtener/stowage/internal/store/migrations"
	"github.com/hurtener/stowage/internal/version"
	// register drivers via init()
	_ "github.com/hurtener/stowage/internal/gateway/bifrost" // SDK driver: all providers in-process (D-049)
	_ "github.com/hurtener/stowage/internal/gateway/mock"
	_ "github.com/hurtener/stowage/internal/gateway/openaicompat" // OpenAI-compatible HTTP client (D-040)
	_ "github.com/hurtener/stowage/internal/store/pgstore"
	_ "github.com/hurtener/stowage/internal/store/sqlitestore"
	_ "github.com/hurtener/stowage/internal/vindex/hnsw" // register "hnsw" vindex driver (D-048)
)

const usage = `stowage — memory infrastructure for agentic systems

Usage:
  stowage <command> [flags]

Commands:
  config    configuration utilities (explain)
  serve     run the HTTP memory service
  mcp       run the MCP tool server
  migrate   apply store schema migrations
  eval      run the evaluation harness
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
		runMCP(os.Args[2:])

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

const mcpUsage = `stowage mcp — run the MCP tool server (Phase 16)

Usage:
  stowage mcp [--config path] [--http addr]

Flags:
  --config path   path to config file (default: auto-discover)
  --http addr     serve streamable-HTTP on addr instead of stdio (e.g. :7162)
`

// runMCP implements `stowage mcp [--config path] [--http addr]`.
//
// Boot sequence mirrors runServe (Steps 1–9 of the serve boot doc) but omits
// the HTTP API server and instead starts the Dockyard MCP server.
//
// Transport selection (AC-4 / D-020):
//   - Default (no --http): stdio — ScopeFn is fixed to cfg.MCP.StdioTenant.
//   - --http <addr>: streamable-HTTP — ScopeFn reads the scope from context
//     (wired by KeyringMiddleware in HTTP mode — store-backed keys, D-030).
func runMCP(args []string) {
	var (
		configPath string
		httpAddr   string
	)
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--config":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "stowage mcp: --config requires a path argument")
				os.Exit(2)
			}
			configPath = args[i+1]
			i++
		case "--http":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "stowage mcp: --http requires an address argument")
				os.Exit(2)
			}
			httpAddr = args[i+1]
			i++
		case "--help", "-h":
			_, _ = fmt.Fprint(os.Stdout, mcpUsage)
			os.Exit(0)
		default:
			fmt.Fprintf(os.Stderr, "stowage mcp: unknown flag %q\n\n%s", args[i], mcpUsage)
			os.Exit(2)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	cfg, err := config.Load(ctx, configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "stowage mcp: load config: %v\n", err)
		os.Exit(1)
	}
	if err := cfg.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "stowage mcp: invalid config: %v\n", err)
		os.Exit(1)
	}

	// Boot the core stack (telemetry, store, gateway, vindex, embedder, retriever,
	// topics, grants). Use context.Background() — NOT the signal ctx — so the
	// embedder worker, pipeline stages, and sweeps live for the process lifetime
	// and are torn down by the graceful Drain + Close below, exactly as `serve`
	// and the embedded SDK do. Passing the signal ctx here would cancel the
	// embedder at SIGTERM, BEFORE Drain flushes the reconcile stage, so records
	// drained at shutdown would lose their embeddings (boot.Open's ctx governs the
	// embedder goroutine and Close does not stop it). Aligning the three paths
	// here closes that lifecycle divergence (D-067 lens).
	stk, err := boot.Open(context.Background(), cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "stowage mcp: boot: %v\n", err)
		os.Exit(1)
	}
	defer func() {
		if closeErr := stk.Close(context.Background()); closeErr != nil {
			stk.Log.Error("stowage mcp: close stack", "err", closeErr)
		}
	}()
	slog.SetDefault(stk.Log)

	// Start the live derivation system — the identical buffer/extract/reconcile
	// pipeline, lifecycle sweeps, and embedding backfill that `stowage serve` and
	// the SDK run (D-068). Without this, MCP-ingested records durably appended but
	// never became memories (the flagship parity blocker, BUG-1).
	//
	// context.Background() (not the signal ctx) for the same reason as boot.Open
	// above: the stages drain on channel close (Drain) and the sweeps stop via
	// Drain, so they need a lifetime independent of the shutdown signal — matching
	// `serve`. Shutdown is driven by ServeStdio(ctx) / httpSrv.Shutdown reacting
	// to the signal ctx, then the deferred Drain.
	p, err := boot.StartPipeline(context.Background(), stk, *cfg)
	if err != nil {
		stk.Log.Error("stowage mcp: start pipeline", "err", err)
		os.Exit(1)
	}
	defer func() {
		// ctx is cancelled on signal; use a fresh bounded context for drain.
		drainCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := p.Drain(drainCtx); err != nil { //nolint:contextcheck // parent ctx is intentionally done at shutdown
			stk.Log.Error("stowage mcp: drain pipeline", "err", err)
		}
	}()

	// ScopeFn: stdio uses a fixed tenant; HTTP mode resolves from context.
	var scopeFn mcpserver.ScopeFn
	if httpAddr != "" {
		scopeFn = mcpserver.CtxScopeFn() // tenant from the authenticated key (KeyringMiddleware)
	} else {
		scopeFn = mcpserver.StdioScopeFn(cfg.MCP.StdioTenant)
	}

	svc := &mcpserver.Services{
		Store:         stk.Store,
		Retriever:     stk.Retriever,
		TopicSvc:      stk.TopicSvc,
		GrantsSvc:     stk.GrantsSvc,
		PipelineIn:    p.In,
		PipelineStage: p.Stage,
		Log:           stk.Log,
		ScopeFn:       scopeFn,
		Profile:       cfg.Profile,
	}

	srv, err := mcpserver.New(server.Info{
		Name:    "stowage",
		Title:   "Stowage Memory MCP Server",
		Version: version.Version,
	}, svc)
	if err != nil {
		stk.Log.Error("stowage mcp: create server", "err", err)
		os.Exit(1)
	}

	stk.Log.Info("stowage mcp: ready", "tools", len(srv.Tools()), "transport", map[bool]string{true: "http:" + httpAddr, false: "stdio"}[httpAddr != ""])

	if httpAddr != "" {
		handler, hErr := srv.HTTPHandler(nil)
		if hErr != nil {
			stk.Log.Error("stowage mcp: http handler", "err", hErr)
			os.Exit(1)
		}
		httpSrv := &http.Server{
			Addr:              httpAddr,
			Handler:           mcpserver.KeyringMiddleware(stk.Store.Keys(), handler),
			ReadHeaderTimeout: 10 * time.Second,
		}
		// shutdownDone is closed only after httpSrv.Shutdown FINISHES draining
		// in-flight handlers. ListenAndServe returns as soon as Shutdown CLOSES
		// the listeners — not when in-flight handlers complete — so without this
		// barrier the deferred p.Drain could close the ingest channel while an
		// MCP handler is still in its non-blocking enqueue (a send on a closed
		// channel, a panic across the MCP boundary). `serve` gets this right by
		// calling srv.Shutdown synchronously before p.Drain; mirror that here by
		// awaiting Shutdown before this function returns and the defers run.
		shutdownDone := make(chan struct{})
		go func() {
			<-ctx.Done()
			// ctx is already cancelled here; a fresh background context is correct
			// for the graceful shutdown timeout — the parent is intentionally done.
			shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = httpSrv.Shutdown(shutCtx) //nolint:contextcheck
			close(shutdownDone)
		}()
		if listenErr := httpSrv.ListenAndServe(); listenErr != nil && !errors.Is(listenErr, http.ErrServerClosed) {
			stk.Log.Error("stowage mcp: http serve", "err", listenErr)
			os.Exit(1)
		}
		// ListenAndServe returned because Shutdown closed the listeners; wait for
		// Shutdown to finish draining in-flight handlers before the deferred Drain
		// closes the ingest channel (ingress-before-Drain, no send-on-closed race).
		<-shutdownDone
	} else {
		if serveErr := srv.ServeStdio(ctx); serveErr != nil && !isCleanMCPExit(serveErr) {
			stk.Log.Error("stowage mcp: stdio serve", "err", serveErr)
			os.Exit(1)
		}
	}

	stk.Log.Info("stowage mcp: stopped")
}

// isCleanMCPExit reports whether err represents a normal MCP server exit that
// should not propagate as an error:
//   - io.EOF: stdin closed by the client (normal stdio session end).
//   - context.Canceled / context.DeadlineExceeded: SIGTERM / timeout.
//   - "server is closing: EOF": the go-sdk's error for a clean stdin close;
//     the jsonrpc2 wire layer wraps io.EOF in a custom error type that does
//     not implement Unwrap, so errors.Is(err, io.EOF) misses it — we fall
//     back to the string suffix as a belt-and-suspenders check.
func isCleanMCPExit(err error) bool {
	if errors.Is(err, io.EOF) ||
		errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	// Belt-and-suspenders: match the jsonrpc2 "server is closing: EOF" error
	// that the SDK produces when the stdio transport hits EOF on stdin.
	msg := err.Error()
	return len(msg) >= 3 && msg[len(msg)-3:] == "EOF"
}

const serveUsage = `stowage serve — run the HTTP memory service

Usage:
  stowage serve [--config path]

Flags:
  --config path   path to config file (default: auto-discover)
`

// runServe implements `stowage serve [--config path]`.
//
// Boot sequence:
//  1. config.Load          — typed config, fail-loud validation
//  2. boot.Open            — telemetry, store+migrate, gateway+probe, vindex,
//     embedder, retriever, topics, grants (static stack)
//  3. api.New              — build HTTP server with all routes
//  4. boot.StartPipeline   — the live derivation system (buffer/extract/reconcile
//     stages + lifecycle sweeps + embedding backfill); the
//     single canonical post-boot wiring shared with
//     `stowage mcp` and the SDK (D-068)
//  5. srv.Set*             — wire the HTTP surface onto the live system
//  6. (optional) mcpserver — when cfg.Server.MCPListen != "", co-mount the
//     MCP-over-HTTP surface on a SECOND listener over the SAME stk + p (one
//     cache, one pipeline — the D-073/D-074 canonical both-surfaces shape)
//  7. ListenAndServe       — start accepting connections on both listeners
//
// Graceful shutdown on SIGTERM/SIGINT:
//  1. api.Shutdown (+ mcpHTTP.Shutdown when co-mounted) — stop accepting on both
//     listeners, await in-flight handlers (no further ingest enqueues)
//  2. p.Drain      — stop sweeps + backfill, close channel, drain the stages
//  3. stk.Close    — retriever/gateway/store close (via defer)
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

	// Boot the core stack (telemetry, store, gateway, vindex, embedder, retriever,
	// topics, grants). ctx is context.Background() so the embedder runs for the
	// process lifetime; shutdown is handled by the graceful drain below.
	stk, err := boot.Open(ctx, cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "stowage serve: boot: %v\n", err)
		os.Exit(1)
	}
	// Store close happens inside stk.Close (last in reverse order); keep the
	// existing defer pattern for serve by deferring stk.Close.
	defer func() {
		if closeErr := stk.Close(context.Background()); closeErr != nil {
			stk.Log.Error("stowage serve: close stack", "err", closeErr)
		}
	}()
	slog.SetDefault(stk.Log)

	// Expose additional metrics (reg is returned by boot for API server wiring).
	_ = prometheus.NewRegistry() // noop: reg already registered inside boot

	srv, err := api.New(cfg, stk.Store, stk.Log, stk.Metrics)
	if err != nil {
		stk.Log.Error("stowage serve: api.New", "err", err)
		os.Exit(1)
	}

	// Start the live derivation system — buffer/extract/reconcile stages, the
	// lifecycle sweeps, and the embedding backfill — via the single canonical
	// post-boot wiring shared with `stowage mcp` and the SDK (D-068). No stage
	// is constructed directly here; StartPipeline owns the ingest channel.
	p, err := boot.StartPipeline(ctx, stk, *cfg)
	if err != nil {
		stk.Log.Error("stowage serve: start pipeline", "err", err)
		os.Exit(1)
	}

	// Wire the HTTP surface onto the live system.
	srv.SetPipelineIn(p.In) // ingest enqueues onto the shared channel
	srv.SetStage(p.Stage)   // buffer flush / branch control
	srv.SetTopicService(stk.TopicSvc)
	srv.SetRetriever(stk.Retriever)
	srv.SetGrantsService(stk.GrantsSvc)

	// Optional co-mounted MCP-over-HTTP surface (D-074). When server.mcp_listen
	// is set, serve the SAME mcpserver handlers (h3/h4/h5) over the SAME
	// stk + p — one result cache, one pipeline, no cross-process staleness
	// (the D-073 canonical one-process/both-surfaces shape). Built here, before
	// the listeners start, so a build error exits before any port binds. A
	// SEPARATE http.Server (not a path-prefix on the api listener) because MCP
	// streams and must NOT inherit the REST WriteTimeout/middleware — so it sets
	// only ReadHeaderTimeout, mirroring `stowage mcp --http`. mcpHTTP stays nil
	// when the knob is empty: `stowage serve` then binds exactly one port,
	// unchanged.
	var mcpHTTP *http.Server
	if cfg.Server.MCPListen != "" {
		mcpSvc := &mcpserver.Services{
			Store:         stk.Store,
			Retriever:     stk.Retriever,
			TopicSvc:      stk.TopicSvc,
			GrantsSvc:     stk.GrantsSvc,
			PipelineIn:    p.In,    // SAME ingest channel as the HTTP API
			PipelineStage: p.Stage, // SAME buffer stage (flush/branch control)
			Log:           stk.Log,
			ScopeFn:       mcpserver.CtxScopeFn(), // tenant from the authenticated key
			Profile:       cfg.Profile,
		}
		mcpSrv, mcpErr := mcpserver.New(server.Info{
			Name:    "stowage",
			Title:   "Stowage Memory MCP Server",
			Version: version.Version,
		}, mcpSvc)
		if mcpErr != nil {
			stk.Log.Error("stowage serve: create mcp server", "err", mcpErr)
			os.Exit(1)
		}
		mcpHandler, hErr := mcpSrv.HTTPHandler(nil)
		if hErr != nil {
			stk.Log.Error("stowage serve: mcp http handler", "err", hErr)
			os.Exit(1)
		}
		mcpHTTP = &http.Server{
			Addr:              cfg.Server.MCPListen,
			Handler:           mcpserver.KeyringMiddleware(stk.Store.Keys(), mcpHandler),
			ReadHeaderTimeout: 10 * time.Second, // no WriteTimeout — MCP streams
		}
	}

	// Start HTTP server in a goroutine.
	servErr := make(chan error, 1)
	go func() {
		if listenErr := srv.ListenAndServe(); listenErr != nil && !errors.Is(listenErr, http.ErrServerClosed) {
			servErr <- listenErr
		}
	}()

	// Start the co-mounted MCP listener in a goroutine; surface listen errors
	// the same way as the api one.
	if mcpHTTP != nil {
		go func() {
			if listenErr := mcpHTTP.ListenAndServe(); listenErr != nil && !errors.Is(listenErr, http.ErrServerClosed) {
				servErr <- listenErr
			}
		}()
		stk.Log.Info("stowage serve: mcp co-mounted", "addr", cfg.Server.MCPListen)
	}

	stk.Log.Info("stowage serve: ready", "addr", cfg.Server.Listen)

	// Wait for termination signal or server error.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

	select {
	case sig := <-sigCh:
		stk.Log.Info("stowage serve: shutting down", "signal", sig)
	case err := <-servErr:
		stk.Log.Error("stowage serve: server error", "err", err)
		os.Exit(1)
	}

	// Graceful shutdown:
	//  1. api.Shutdown + mcpHTTP.Shutdown — stop accepting on BOTH listeners and
	//     await in-flight handlers; once both return, no surface can enqueue ingest.
	//  2. p.Drain      — stop sweeps + backfill (the ingest-channel producers),
	//                    close the channel, then drain buffer → extract → reconcile.
	//  3. stk.Close (deferred above) — retriever.Close, gateway.Close, store.Close
	//
	// Both listeners MUST be fully shut down BEFORE p.Drain closes the ingest
	// channel — otherwise an in-flight MCP/REST handler could enqueue onto a
	// closed channel (a send on a closed channel, a panic across the boundary;
	// the h1 ingress-before-Drain invariant).
	shutdownCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		stk.Log.Error("stowage serve: shutdown", "err", err)
	}
	if mcpHTTP != nil {
		if err := mcpHTTP.Shutdown(shutdownCtx); err != nil {
			stk.Log.Error("stowage serve: mcp shutdown", "err", err)
		}
	}
	if err := p.Drain(shutdownCtx); err != nil {
		stk.Log.Error("stowage serve: drain pipeline", "err", err)
	}
	stk.Log.Info("stowage serve: stopped")
	// stk.Close runs via defer above (gateway.Close, store.Close included).
}
