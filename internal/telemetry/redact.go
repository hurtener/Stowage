package telemetry

import (
	"context"
	"log/slog"
)

const redactedValue = "[REDACTED]"

// DefaultDenySet returns the set of attribute keys whose values are masked
// by RedactingHandler (AC-7).
func DefaultDenySet() map[string]bool {
	return map[string]bool{
		"api_key":       true,
		"authorization": true,
		"dsn":           true,
		"secret":        true,
	}
}

// RedactingHandler wraps an slog.Handler and replaces the value of any
// attribute whose key is in the deny-set with "[REDACTED]".
// Safe for concurrent use (immutable after construction).
type RedactingHandler struct {
	inner   slog.Handler
	denySet map[string]bool
}

// NewRedactingHandler wraps inner with the provided deny-set.
// denySet must not be modified after construction.
func NewRedactingHandler(inner slog.Handler, denySet map[string]bool) *RedactingHandler {
	return &RedactingHandler{inner: inner, denySet: denySet}
}

// Enabled delegates to the inner handler.
func (h *RedactingHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

// Handle redacts deny-set attributes before passing the record to inner.
func (h *RedactingHandler) Handle(ctx context.Context, r slog.Record) error {
	r2 := slog.NewRecord(r.Time, r.Level, r.Message, r.PC)
	r.Attrs(func(a slog.Attr) bool {
		r2.AddAttrs(h.redactAttr(a))
		return true
	})
	return h.inner.Handle(ctx, r2)
}

// WithAttrs redacts deny-set attributes in the with-attrs set and delegates.
func (h *RedactingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	redacted := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		redacted[i] = h.redactAttr(a)
	}
	return &RedactingHandler{inner: h.inner.WithAttrs(redacted), denySet: h.denySet}
}

// WithGroup delegates to inner, preserving redaction.
func (h *RedactingHandler) WithGroup(name string) slog.Handler {
	return &RedactingHandler{inner: h.inner.WithGroup(name), denySet: h.denySet}
}

func (h *RedactingHandler) redactAttr(a slog.Attr) slog.Attr {
	if h.denySet[a.Key] {
		return slog.String(a.Key, redactedValue)
	}
	return a
}
