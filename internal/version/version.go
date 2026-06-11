// Package version exposes the build version of the stowage binary.
// The value is injected at build time via -ldflags (see Makefile).
package version

// Version is the semantic version of this build, or "dev" for local builds.
var Version = "dev"
