// Command stowage is the Stowage memory server: an HTTP service, an MCP
// server, and an operations CLI in one CGo-free static binary (RFC §2).
//
// Subcommands land with their phases (docs/plans/README.md); until then they
// report their status and exit non-zero so smoke scripts can assert on them.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/hurtener/stowage/internal/config"
	"github.com/hurtener/stowage/internal/version"
)

const usage = `stowage — memory infrastructure for agentic systems

Usage:
  stowage <command> [flags]

Commands:
  config    configuration utilities (explain)
  serve     run the HTTP memory service        (lands in Phase 05)
  mcp       run the MCP tool server            (lands in Phase 17)
  migrate   apply store migrations             (lands in Phase 03)
  eval      run the evaluation harness         (lands in Phase 13)
  version   print the build version
`

const configUsage = `stowage config — configuration utilities

Usage:
  stowage config <subcommand> [flags]

Subcommands:
  explain [--config path]   print effective config with provenance
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

	case "serve", "mcp", "migrate", "eval":
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
