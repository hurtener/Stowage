package api

import (
	"io"
	"log/slog"
)

// noopLogger returns a slog.Logger that discards all output.
// Used in tests that exercise middleware without a real server.
func noopLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
