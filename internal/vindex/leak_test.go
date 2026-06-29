// Package vindex_test provides the goroutine-leak gate for the vindex package
// (D-126). Advisory by default: a detected leak is logged to stderr but does
// not fail the test binary. Promote to Strict once the vindex goroutine
// lifecycle is fully characterised.
package vindex_test

import (
	"testing"

	"github.com/hurtener/stowage/internal/leakcheck"
)

func TestMain(m *testing.M) {
	leakcheck.Run(m, leakcheck.Advisory)
}
