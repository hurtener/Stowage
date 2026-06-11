// Package telemetry wires slog and Prometheus for Stowage.
//
// New returns a configured logger (JSON in prod, text in dev) wrapped in a
// RedactingHandler, plus a Prometheus registry. No secrets appear in logs.
package telemetry

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"

	"github.com/hurtener/stowage/internal/identity"
)

// Config is the minimal view of telemetry configuration needed by New.
type Config struct {
	LogLevel      string // "debug" | "info" | "warn" | "error"
	LogFormat     string // "json" | "text"
	MetricsListen string // e.g. ":7161" (unused by New; for caller to wire)
}

// New builds a configured *slog.Logger and *prometheus.Registry.
// The logger is wrapped in a RedactingHandler (AC-7).
// The registry includes Go runtime and process metrics.
func New(cfg Config) (*slog.Logger, *prometheus.Registry, error) {
	var level slog.Level
	if err := level.UnmarshalText([]byte(cfg.LogLevel)); err != nil {
		return nil, nil, fmt.Errorf("telemetry: invalid log level %q: %w", cfg.LogLevel, err)
	}

	opts := &slog.HandlerOptions{Level: level}
	var base slog.Handler
	switch cfg.LogFormat {
	case "json":
		base = slog.NewJSONHandler(os.Stderr, opts)
	default:
		base = slog.NewTextHandler(os.Stderr, opts)
	}
	handler := NewRedactingHandler(base, DefaultDenySet())
	logger := slog.New(handler)

	reg := prometheus.NewRegistry()
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)

	return logger, reg, nil
}

// With returns a child logger annotated with the scope attributes from ctx.
// If no scope is in ctx, the logger is returned unchanged.
func With(ctx context.Context, logger *slog.Logger) *slog.Logger {
	scope, err := identity.FromContext(ctx)
	if err != nil {
		return logger
	}
	attrs := []any{
		slog.String("tenant", scope.Tenant),
	}
	if scope.Project != "" {
		attrs = append(attrs, slog.String("project", scope.Project))
	}
	if scope.User != "" {
		attrs = append(attrs, slog.String("user", scope.User))
	}
	if scope.Session != "" {
		attrs = append(attrs, slog.String("session", scope.Session))
	}
	return logger.With(attrs...)
}
