// Command stowage is the Stowage memory server: an HTTP service, an MCP
// server, and an operations CLI in one CGo-free static binary (RFC §2).
//
// Subcommands land with their phases (docs/plans/README.md); until then they
// report their status and exit non-zero so smoke scripts can assert on them.
package main

import (
	"bytes"
	"context"
	"encoding/json"
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

	"github.com/hurtener/stowage/eval/datasets"
	_ "github.com/hurtener/stowage/eval/datasets/locomo"      // registers "locomo" (D-096)
	_ "github.com/hurtener/stowage/eval/datasets/longmemeval" // registers "longmemeval" + "longmemeval_s" (D-096)
	"github.com/hurtener/stowage/internal/api"
	"github.com/hurtener/stowage/internal/auth"
	"github.com/hurtener/stowage/internal/boot"
	"github.com/hurtener/stowage/internal/config"
	"github.com/hurtener/stowage/internal/identity"
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
                           known datasets: longmemeval, longmemeval_s, locomo
  ci                       print instructions for running the CI eval gate
`

const evalFetchUsage = `stowage eval fetch — download an eval dataset into eval/data/

Usage:
  stowage eval fetch --dataset <name> [--data-dir path]

Flags:
  --dataset name    dataset to fetch (longmemeval | longmemeval_s | locomo)
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
	spec, err := datasets.MustLookup(dataset)
	if err != nil {
		fmt.Fprintf(os.Stderr, "stowage eval fetch: %v\n", err)
		os.Exit(2)
	}
	dest, err := spec.Fetch(ctx, dataDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "stowage eval fetch: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("stowage eval fetch: %s saved to %s\n", dataset, dest)
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
		catalog    = "agent"
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
		case "--catalog":
			if i+1 >= len(args) || (args[i+1] != "agent" && args[i+1] != "full") {
				fmt.Fprintln(os.Stderr, "stowage mcp: --catalog must be agent or full")
				os.Exit(2)
			}
			catalog = args[i+1]
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

	// Build the ONE *auth.Authenticator this process's HTTP-mode MCP surface
	// uses (D-067). ModeJWT's JWKS fetch is SYNCHRONOUS here, at boot — a
	// JWKS-unreachable jwt-mode config fails LOUD (D-147); there is no silent
	// keyring fallback. (Unused in stdio mode — StdioScopeFn never calls it —
	// but built unconditionally to keep the boot sequence uniform with `serve`.)
	authn, err := buildAuthenticator(context.Background(), cfg.Auth, stk.Store.Keys())
	if err != nil {
		stk.Log.Error("stowage mcp: build authenticator", "err", err)
		os.Exit(1)
	}

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
		Store:              stk.Store,
		Retriever:          stk.Retriever,
		TopicSvc:           stk.TopicSvc,
		GrantsSvc:          stk.GrantsSvc,
		ViewsSvc:           stk.ViewsSvc,
		Gateway:            stk.Gateway,
		TraceSigner:        stk.TraceSigner,
		PipelineIn:         p.In,
		PipelineStage:      p.Stage,
		Log:                stk.Log,
		ScopeFn:            scopeFn,
		Profile:            cfg.Profile,
		BrowseDefaultLimit: cfg.Retrieval.BrowseDefaultLimit,
		ResolveOpts: identity.ResolveOptions{
			Posture:      identity.ParsePosture(cfg.Retrieval.ReadPosture),
			Multiplexing: cfg.Identity.Multiplexing,
		},
	}

	constructor := mcpserver.New
	if httpAddr == "" && catalog == "agent" {
		constructor = mcpserver.NewAgent
	}
	srv, err := constructor(server.Info{
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
		handler, hErr := srv.HTTPHandler(mcpHTTPOptions(cfg.Auth.Mode, cfg.Server.MCPTrustProxy))
		if hErr != nil {
			stk.Log.Error("stowage mcp: http handler", "err", hErr)
			os.Exit(1)
		}
		handler, hErr = withAgentMCP(handler, svc, cfg.Auth.Mode, cfg.Server.MCPTrustProxy)
		if hErr != nil {
			stk.Log.Error("stowage mcp: agent catalog", "err", hErr)
			os.Exit(1)
		}
		httpSrv := &http.Server{
			Addr:              httpAddr,
			Handler:           mcpAccessLog(stk.Log, mcpAuthHandler(cfg.Auth.Mode, authn, handler)),
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

// buildAuthenticator constructs the ONE *auth.Authenticator both HTTP seams
// (the REST API and the MCP-over-HTTP surface) share (D-067), selected by
// cfg.Mode. This is the single canonical wiring point `stowage serve` and
// `stowage mcp --http` both call.
//
// ModeKeyring (the default): wraps the store keyring — byte-identical to
// pre-ae7 behavior (zero-config preserved, D-034).
//
// ModeJWT: builds the JWKS/static KeySet with a SYNCHRONOUS fetch, then the
// Validator. A JWKS-unreachable source (bad URL, unparseable file, zero
// usable asymmetric keys) makes this call return a non-nil error, which the
// caller treats as a FATAL boot error — `stowage serve`/`stowage mcp` never
// boot into a jwt-mode configuration they cannot enforce, and never silently
// fall back to the keyring (D-147).
func buildAuthenticator(ctx context.Context, cfg config.AuthConfig, keyring auth.Keyring) (*auth.Authenticator, error) {
	if cfg.Mode != string(auth.ModeJWT) {
		return auth.NewKeyringAuthenticator(keyring), nil
	}

	keys, err := auth.NewJWKSKeySet(ctx, auth.JWKSSource{URL: cfg.JWKS.URL, File: cfg.JWKS.File}, time.Duration(cfg.JWKS.MaxStale)*time.Second)
	if err != nil {
		return nil, fmt.Errorf("auth: jwt mode: build JWKS key set: %w", err)
	}

	var opts []auth.Option
	if cfg.Issuer != "" {
		opts = append(opts, auth.WithIssuer(cfg.Issuer))
	}
	if cfg.Audience != "" {
		opts = append(opts, auth.WithAudience(cfg.Audience))
	}
	if algs := cfg.AlgorithmList(); len(algs) > 0 {
		opts = append(opts, auth.WithAlgorithms(algs))
	}

	v, err := auth.NewValidator(keys, opts...)
	if err != nil {
		return nil, fmt.Errorf("auth: jwt mode: build validator: %w", err)
	}
	return auth.NewJWTAuthenticator(v), nil
}

// mcpAuthHandler selects the MCP-over-HTTP auth middleware by auth mode (D-152),
// used identically at BOTH MCP-over-HTTP wiring points (`stowage mcp --http` and
// the `stowage serve` co-mounted MCP port) so the two surfaces behave the same:
//
//   - jwt mode: MethodAwareAuthMiddleware — the identity-free handshake methods
//     (initialize, notifications/initialized, ping, tools/list, the SSE GET leg,
//     session DELETE) are served unauthenticated; every tools/call and resource
//     operation still requires the per-call bearer. This lets an MCP host that
//     attaches connections user-agnostically (no per-user credential at connect
//     time) and injects a per-user bearer per call complete the handshake.
//   - keyring mode: the strict AuthMiddleware, byte-identical to today — a
//     keyring client owns its static credential at connect time, so there is no
//     reason to open its handshake.
func mcpAuthHandler(mode string, authn *auth.Authenticator, next http.Handler) http.Handler {
	if mode == string(auth.ModeJWT) {
		return mcpserver.MethodAwareAuthMiddleware(authn, next)
	}
	return mcpserver.AuthMiddleware(authn, next)
}

// mcpHTTPOptions selects the streamable-HTTP transport options for the MCP
// surface by auth mode (D-152), used identically at BOTH MCP-over-HTTP wiring
// points. In jwt mode the transport is STATELESS, and this is REQUIRED for
// correctness — not a tuning knob:
//
// The per-call bearer's identity must reach the tool handler on EACH request
// (D-152: "sessions never cache a scope; auth is per-HTTP-request"). But the
// go-sdk's STATEFUL streamable transport binds a session's tool-handler context
// to the request that established the session — the bearer-less, open
// `initialize` (server.Connect(req.Context(), …)). It therefore caches that
// request's (empty) scope for the session's whole life, and a per-call bearer
// injected on a later `tools/call` POST lands on a request context the handler
// never sees — stranding the identity (the handler resolves "no authenticated
// scope"). Stateless serves each POST under its own request context, so the
// per-call scope resolves. Stowage's MCP surface is tools-only (no
// server-initiated sampling/elicitation/roots), so statelessness costs nothing.
//
// Security posture is unchanged: a non-nil *HTTPOptions whose Security is the
// zero value still resolves to DefaultHTTPSecurity (dockyard's
// HTTPOptions.security()), identical to the nil default.
//
// Keyring mode keeps the stateful default (nil) — byte-identical to today: a
// keyring client presents its static credential on every request, including
// initialize, so the session context carries the scope and nothing is stranded.
//
// trustProxy (server.mcp_trust_proxy, D-156) relaxes the transport's
// DNS-rebinding LOCALHOST guard for deployment behind a trusted reverse proxy
// (Render/Heroku/Fly, a load balancer). Such a proxy terminates TLS at the edge
// and forwards to the container over a loopback address, so the SDK guard — which
// rejects a request whose local socket address is loopback but whose Host header
// is non-localhost — 403s every request to the public domain. When true we set an
// EXPLICIT HTTPSecurity with DNS-rebinding OFF but cross-origin (CSRF) and
// Content-Type verification ON, so only the localhost guard is dropped. When
// false the options resolve to the SDK's secure DefaultHTTPSecurity exactly as
// before (nil in keyring mode, zero-Security in jwt mode).
func mcpHTTPOptions(mode string, trustProxy bool) *server.HTTPOptions {
	jwt := mode == string(auth.ModeJWT)
	if !jwt && !trustProxy {
		return nil // exact prior keyring default → DefaultHTTPSecurity (all on)
	}
	opts := &server.HTTPOptions{Stateless: jwt}
	if trustProxy {
		opts.Security = server.HTTPSecurity{
			CrossOriginProtection:   true,
			ContentTypeVerification: true,
			// DNSRebindingProtection deliberately OFF: the trusted proxy sets the
			// Host and terminates the public edge (D-156).
		}
	}
	return opts
}

// mcpRootRewrite normalizes a co-mounted MCP request's URL path to "/" before it
// reaches the streamable handler (D-155). MCP-over-HTTP dispatches JSON-RPC on the
// request body, not the URL path, and the ae11 handshake-auth classifier peeks the
// body — neither routes on the path. Rewriting to "/" makes a co-mounted "/mcp"
// (or "/mcp/…") request byte-identical, at the handler, to the same request on a
// dedicated listener, so the shared and separate shapes behave the same. The
// request is cloned (the URL is deep-copied by Clone) so the shared mux's routing
// state is untouched.
func mcpRootRewrite(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/"
		r2.URL.RawPath = ""
		next.ServeHTTP(w, r2)
	})
}

// restDeadlineHandler re-imposes the REST read/write bound per-request when the
// REST surface is co-mounted on a shared http.Server that itself sets no
// ReadTimeout/WriteTimeout (D-155). The shared server must leave those unset so the
// co-mounted MCP subtree can stream (SSE + long tool calls) — the whole reason the
// default shape uses two listeners (D-074). Setting the deadline per-request via
// http.ResponseController preserves the REST protection without imposing it on the
// MCP subtree. A zero duration leaves that bound unset; a driver that does not
// support deadlines simply keeps the shared-server default (best-effort).
func restDeadlineHandler(read, write time.Duration, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rc := http.NewResponseController(w)
		now := time.Now()
		if read > 0 {
			_ = rc.SetReadDeadline(now.Add(read))
		}
		if write > 0 {
			_ = rc.SetWriteDeadline(now.Add(write))
		}
		next.ServeHTTP(w, r)
	})
}

// mcpAccessLog wraps the MCP-over-HTTP handler with an access-log line, giving the
// MCP surface parity with the REST "api: request" log. The MCP handler is mounted
// OUTSIDE the api's request-log middleware (its own listener in separate mode, its
// own mux branch in co-mount) so its traffic was previously invisible; this makes
// it observable. Unlike the REST log, it names the JSON-RPC method (and the tool
// for tools/call) so an operator sees WHICH tool a runtime invoked, not just
// "POST /mcp". Applied outermost (over the auth wrapper) so the logged status
// reflects auth verdicts too. next MUST be nil-safe callers' responsibility; a nil
// logger returns next unwrapped.
func mcpAccessLog(log *slog.Logger, next http.Handler) http.Handler {
	if log == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rpcMethod, tool := peekMCPMethod(r)
		rec := &mcpStatusRecorder{ResponseWriter: w, status: http.StatusOK}
		start := time.Now()
		next.ServeHTTP(rec, r)
		attrs := []any{"method", r.Method, "rpc", rpcMethod}
		if tool != "" {
			attrs = append(attrs, "tool", tool)
		}
		attrs = append(attrs, "status", rec.status, "dur_ms", time.Since(start).Milliseconds())
		log.Info("mcp: request", attrs...)
	})
}

// mcpStatusRecorder captures the response status for the access log. It implements
// Unwrap so http.ResponseController (used by the streamable transport to Flush SSE
// frames, streamable.go) reaches the real ResponseWriter — the recorder never
// intercepts the flush path, so streaming is unaffected.
type mcpStatusRecorder struct {
	http.ResponseWriter
	status int
}

func (m *mcpStatusRecorder) WriteHeader(code int) {
	m.status = code
	m.ResponseWriter.WriteHeader(code)
}

func (m *mcpStatusRecorder) Unwrap() http.ResponseWriter { return m.ResponseWriter }

// peekMCPMethod reads the JSON-RPC method (and, for tools/call, the tool name) from
// a POST body WITHOUT consuming it: it buffers a bounded prefix, reconstructs
// r.Body so the downstream handler reads the full stream unchanged, and does a
// minimal decode. A batch array logs as "batch"; a truncated/undecodable prefix
// (rare — MCP frames are small) logs an empty rpc. GET (SSE stream) and DELETE
// (session teardown) carry no method.
func peekMCPMethod(r *http.Request) (rpcMethod, tool string) {
	if r.Body == nil || r.Method != http.MethodPost {
		return "", ""
	}
	const peekCap = 64 << 10 // matches the handshake classifier's body peek
	orig := r.Body
	buf, _ := io.ReadAll(io.LimitReader(orig, peekCap))
	// Reconstruct the body: the buffered prefix, then whatever remains unread (for a
	// body larger than peekCap), closing the original on Close.
	r.Body = readCloser{Reader: io.MultiReader(bytes.NewReader(buf), orig), Closer: orig}

	trimmed := bytes.TrimSpace(buf)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		return "batch", ""
	}
	var frame struct {
		Method string `json:"method"`
		Params struct {
			Name string `json:"name"`
		} `json:"params"`
	}
	if err := json.Unmarshal(buf, &frame); err != nil {
		return "", ""
	}
	return frame.Method, frame.Params.Name
}

// readCloser adapts a separate Reader + Closer into an io.ReadCloser (used to
// reconstruct a peeked request body).
type readCloser struct {
	io.Reader
	io.Closer
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

	// Build the ONE *auth.Authenticator both HTTP seams (the REST API and, if
	// co-mounted below, the MCP-over-HTTP surface) share (D-067). ModeJWT's
	// JWKS fetch is SYNCHRONOUS here, at boot — a JWKS-unreachable jwt-mode
	// config fails LOUD (D-147); there is no silent keyring fallback.
	authn, err := buildAuthenticator(ctx, cfg.Auth, stk.Store.Keys())
	if err != nil {
		stk.Log.Error("stowage serve: build authenticator", "err", err)
		os.Exit(1)
	}
	srv.SetAuthenticator(authn)

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
	srv.SetViewsService(stk.ViewsSvc)   // ae9: /v1/scopes/views admin (D-149/D-151)
	srv.SetGateway(stk.Gateway)         // POST /v1/verify (Phase 25)
	srv.SetTraceSigner(stk.TraceSigner) // GET /v1/traces (Phase 26)

	// Optional MCP-over-HTTP surface (D-074/D-155). Two exposure shapes, both
	// serving the SAME mcpserver handlers (h3/h4/h5) over the SAME stk + p — one
	// result cache, one pipeline, no cross-process staleness (the D-073 canonical
	// one-process/both-surfaces shape):
	//   - "separate" (default): opt-in via server.mcp_listen; MCP binds its OWN
	//     port and never inherits the REST WriteTimeout/middleware — it sets only
	//     ReadHeaderTimeout, mirroring `stowage mcp --http`.
	//   - "shared" (server.mcp_mount=shared): co-mount MCP on the server.listen
	//     port under "/mcp", for single-port platforms (Render/Heroku/Fly). The
	//     invariant holds without a second listener: the combined http.Server sets
	//     no WriteTimeout (so MCP streams) and the REST subtree re-imposes its
	//     write bound per-request (restDeadlineHandler).
	// The handler is built here, before any listener binds, so a build error exits
	// first. mcpHTTPHandler stays nil when MCP is disabled: `stowage serve` then
	// binds exactly one port with the REST surface only, unchanged.
	sharedMCP := cfg.Server.MCPMount == "shared"
	mcpEnabled := sharedMCP || cfg.Server.MCPListen != ""

	var mcpHTTPHandler http.Handler // auth-wrapped MCP handler, ready to mount (nil ⇒ disabled)
	if mcpEnabled {
		mcpSvc := &mcpserver.Services{
			Store:              stk.Store,
			Retriever:          stk.Retriever,
			TopicSvc:           stk.TopicSvc,
			GrantsSvc:          stk.GrantsSvc,
			ViewsSvc:           stk.ViewsSvc,
			Gateway:            stk.Gateway,
			TraceSigner:        stk.TraceSigner,
			PipelineIn:         p.In,    // SAME ingest channel as the HTTP API
			PipelineStage:      p.Stage, // SAME buffer stage (flush/branch control)
			Log:                stk.Log,
			ScopeFn:            mcpserver.CtxScopeFn(), // tenant from the authenticated key
			Profile:            cfg.Profile,
			BrowseDefaultLimit: cfg.Retrieval.BrowseDefaultLimit,
			ResolveOpts: identity.ResolveOptions{
				Posture:      identity.ParsePosture(cfg.Retrieval.ReadPosture),
				Multiplexing: cfg.Identity.Multiplexing,
			},
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
		mcpHandler, hErr := mcpSrv.HTTPHandler(mcpHTTPOptions(cfg.Auth.Mode, cfg.Server.MCPTrustProxy))
		if hErr != nil {
			stk.Log.Error("stowage serve: mcp http handler", "err", hErr)
			os.Exit(1)
		}
		// SAME Authenticator as the REST API (D-067). Method-aware handshake auth
		// in jwt mode (ae11/D-152); strict key auth otherwise.
		dualHandler, agentErr := withAgentMCP(mcpHandler, mcpSvc, cfg.Auth.Mode, cfg.Server.MCPTrustProxy)
		if agentErr != nil {
			stk.Log.Error("stowage serve: agent catalog", "err", agentErr)
			os.Exit(1)
		}
		mcpHTTPHandler = mcpAuthHandler(cfg.Auth.Mode, authn, dualHandler)
	} else {
		// Discoverability hint (a3, D-133): the MCP tool surface is opt-in. Say so on
		// startup so an operator who expected MCP knows both knobs exist, without
		// changing the default single-port REST-only shape (D-074).
		stk.Log.Info("stowage serve: MCP surface disabled — set server.mcp_listen (e.g. :7161) for a second-port co-mount, server.mcp_mount=shared to co-mount on the API port at /mcp, or run `stowage mcp`")
	}

	// Assemble the network listeners from the MCP exposure shape.
	//   - shared: ONE combined http.Server on server.listen serving REST at "/"
	//     and MCP at "/mcp" (single-port platforms, D-155).
	//   - separate: the api's own listener (srv.ListenAndServe) plus, when
	//     server.mcp_listen is set, a dedicated MCP listener (D-074).
	var (
		mcpHTTP *http.Server // separate-mode dedicated MCP listener (nil otherwise)
		apiHTTP *http.Server // shared-mode combined REST+/mcp listener (nil otherwise)
	)
	if sharedMCP {
		idleTimeout := time.Duration(cfg.Server.IdleTimeout) * time.Second
		readTimeout := time.Duration(cfg.Server.ReadTimeout) * time.Second
		writeTimeout := time.Duration(cfg.Server.WriteTimeout) * time.Second

		root := http.NewServeMux()
		// MCP dispatches JSON-RPC on the request body, not the URL path; normalize
		// the co-mount path to "/" so the streamable handler and the handshake-auth
		// classifier (ae11) see exactly the request they would on a dedicated
		// listener. Register both the exact and subtree patterns. mcpAccessLog
		// wraps it so co-mounted MCP traffic is observable (it bypasses the REST
		// request-logger).
		mcpMount := mcpAccessLog(stk.Log, mcpRootRewrite(mcpHTTPHandler))
		root.Handle("/mcp", mcpMount)
		root.Handle("/mcp/", mcpMount)
		// REST owns everything else. The combined server sets no WriteTimeout (so
		// the MCP subtree can stream — D-074's invariant), so REST re-imposes its
		// per-request read/write bound here (D-155).
		root.Handle("/", restDeadlineHandler(readTimeout, writeTimeout, srv))

		apiHTTP = &http.Server{
			Addr:              cfg.Server.Listen,
			Handler:           root,
			ReadHeaderTimeout: 10 * time.Second, // slow-header guard for both subtrees
			IdleTimeout:       idleTimeout,
			MaxHeaderBytes:    1 << 20,
			// Deliberately NO ReadTimeout/WriteTimeout: MCP streams (SSE + long
			// tool calls) and its body is unbounded, exactly as on the dedicated
			// listener. REST bounds are re-imposed per-request by
			// restDeadlineHandler; REST body size stays capped by the api's own
			// bodyLimitMiddleware, which srv still wraps (D-074/D-155).
		}
	} else if cfg.Server.MCPListen != "" {
		mcpHTTP = &http.Server{
			Addr:              cfg.Server.MCPListen,
			Handler:           mcpAccessLog(stk.Log, mcpHTTPHandler),
			ReadHeaderTimeout: 10 * time.Second, // no WriteTimeout — MCP streams
		}
	}

	// Optional dedicated pprof listener (server.pprof_listen). Off by default;
	// NEVER mounted on the public API mux. Admin-gated: process-global profile
	// data is not tenant-scoped (D-126, CLAUDE.md §7).
	var pprofHTTP *http.Server
	if cfg.Server.PprofListen != "" {
		pprofHTTP = &http.Server{
			Addr:              cfg.Server.PprofListen,
			Handler:           srv.PprofAdminHandler(),
			ReadHeaderTimeout: 10 * time.Second,
			// Deliberately NO WriteTimeout: a CPU profile or execution trace
			// capture streams for an operator-chosen duration
			// (/debug/pprof/profile?seconds=N, /debug/pprof/trace?seconds=N) that
			// routinely exceeds any fixed bound; a WriteTimeout would truncate the
			// capture mid-stream. Escaping the REST WriteTimeout is the whole
			// reason this is a separate listener (D-126; same rationale as the MCP
			// listener, D-074). Admin-gated + opt-in + loopback-default keeps the
			// relaxed timeout safe.
		}
	}

	// Start the primary HTTP listener in a goroutine. In shared mode this is the
	// combined REST+/mcp server on server.listen; otherwise it is the api's own
	// listener (REST only, or REST + a separate MCP listener below).
	servErr := make(chan error, 1)
	go func() {
		var listenErr error
		if apiHTTP != nil {
			listenErr = apiHTTP.ListenAndServe()
		} else {
			listenErr = srv.ListenAndServe()
		}
		if listenErr != nil && !errors.Is(listenErr, http.ErrServerClosed) {
			servErr <- listenErr
		}
	}()
	if apiHTTP != nil {
		stk.Log.Info("stowage serve: mcp co-mounted on the API port", "addr", cfg.Server.Listen, "path", "/mcp")
	}

	// Start the dedicated MCP listener in a goroutine (separate mode); surface
	// listen errors the same way as the api one.
	if mcpHTTP != nil {
		go func() {
			if listenErr := mcpHTTP.ListenAndServe(); listenErr != nil && !errors.Is(listenErr, http.ErrServerClosed) {
				servErr <- listenErr
			}
		}()
		stk.Log.Info("stowage serve: mcp co-mounted", "addr", cfg.Server.MCPListen)
	}

	// Start the pprof listener in a goroutine (admin-gated, opt-in — D-126).
	if pprofHTTP != nil {
		go func() {
			if listenErr := pprofHTTP.ListenAndServe(); listenErr != nil && !errors.Is(listenErr, http.ErrServerClosed) {
				servErr <- listenErr
			}
		}()
		stk.Log.Info("stowage serve: pprof listening", "addr", cfg.Server.PprofListen)
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
	// In shared mode the combined listener owns the socket (REST + /mcp). Shut it
	// down FIRST so both subtrees stop accepting and in-flight handlers drain
	// before srv.Shutdown runs the api's post-HTTP cleanup (retriever/injection
	// close). srv.Shutdown still runs in both modes: its own httpSrv.Shutdown is a
	// no-op when apiHTTP served (that server never called ListenAndServe), but the
	// retriever/pipeline cleanup it performs must still happen.
	if apiHTTP != nil {
		if err := apiHTTP.Shutdown(shutdownCtx); err != nil {
			stk.Log.Error("stowage serve: combined listener shutdown", "err", err)
		}
	}
	if err := srv.Shutdown(shutdownCtx); err != nil {
		stk.Log.Error("stowage serve: shutdown", "err", err)
	}
	if mcpHTTP != nil {
		if err := mcpHTTP.Shutdown(shutdownCtx); err != nil {
			stk.Log.Error("stowage serve: mcp shutdown", "err", err)
		}
	}
	if pprofHTTP != nil {
		if err := pprofHTTP.Shutdown(shutdownCtx); err != nil {
			stk.Log.Error("stowage serve: pprof shutdown", "err", err)
		}
	}
	if err := p.Drain(shutdownCtx); err != nil {
		stk.Log.Error("stowage serve: drain pipeline", "err", err)
	}
	stk.Log.Info("stowage serve: stopped")
	// stk.Close runs via defer above (gateway.Close, store.Close included).
}
