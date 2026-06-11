// Command stowage is the Stowage memory server: an HTTP service, an MCP
// server, and an operations CLI in one CGo-free static binary (RFC §2).
//
// Subcommands land with their phases (docs/plans/README.md); until then they
// report their status and exit non-zero so smoke scripts can assert on them.
package main

import (
	"fmt"
	"os"

	"github.com/hurtener/stowage/internal/version"
)

const usage = `stowage — memory infrastructure for agentic systems

Usage:
  stowage <command> [flags]

Commands:
  serve     run the HTTP memory service        (lands in Phase 05)
  mcp       run the MCP tool server            (lands in Phase 16)
  migrate   apply store migrations             (lands in Phase 03)
  eval      run the evaluation harness         (lands in Phase 19)
  version   print the build version
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	switch os.Args[1] {
	case "version":
		fmt.Println(version.Version)
	case "serve", "mcp", "migrate", "eval":
		fmt.Fprintf(os.Stderr, "stowage %s: not implemented yet — see docs/plans/README.md\n", os.Args[1])
		os.Exit(1)
	default:
		fmt.Fprintf(os.Stderr, "stowage: unknown command %q\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}
}
