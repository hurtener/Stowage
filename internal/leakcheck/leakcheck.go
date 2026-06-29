// Package leakcheck wires go.uber.org/goleak into package test suites with an
// advisory-then-promote posture (D-126, Phase P1). In Advisory mode a detected
// goroutine leak is logged but does NOT fail the build; promoting a package to a
// hard gate is a one-line change at its TestMain call site (Advisory -> Strict).
package leakcheck

import (
	"fmt"
	"os"
	"testing"

	"go.uber.org/goleak"
)

// Mode selects advisory (log-only) vs strict (fail-the-build) leak checking.
type Mode int

const (
	Advisory Mode = iota // log leaks, never change the exit code (P1 default)
	Strict               // fail the test binary on a detected leak
)

// Run executes the package's tests, then checks for leaked goroutines. In
// Advisory mode a leak is logged to stderr but the original test exit code is
// preserved; in Strict mode a leak forces a non-zero exit. opts are passed to
// goleak.Find for package-specific ignore rules (e.g. a driver's writer goroutine).
func Run(m *testing.M, mode Mode, opts ...goleak.Option) {
	code := m.Run()
	// Only check for leaks when tests otherwise passed — a failing test often
	// leaves goroutines mid-flight and would produce noisy false positives.
	if code == 0 {
		if err := goleak.Find(opts...); err != nil {
			if mode == Strict {
				fmt.Fprintf(os.Stderr, "leakcheck: goroutine leak detected:\n%v\n", err)
				os.Exit(1)
			}
			fmt.Fprintf(os.Stderr, "leakcheck (ADVISORY — not failing build): goroutine leak detected:\n%v\n", err)
		}
	}
	os.Exit(code)
}
