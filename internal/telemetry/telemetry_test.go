package telemetry_test

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/telemetry"
)

// TestNew verifies New returns a logger and registry without error.
func TestNew(t *testing.T) {
	cfg := telemetry.Config{
		LogLevel:      "info",
		LogFormat:     "text",
		MetricsListen: ":7161",
	}
	logger, reg, err := telemetry.New(cfg)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	if logger == nil {
		t.Fatal("New() logger is nil")
	}
	if reg == nil {
		t.Fatal("New() registry is nil")
	}
}

// TestNewInvalidLevel verifies New rejects unknown log levels.
func TestNewInvalidLevel(t *testing.T) {
	cfg := telemetry.Config{LogLevel: "verbose", LogFormat: "text"}
	_, _, err := telemetry.New(cfg)
	if err == nil {
		t.Fatal("New() = nil error, want error for invalid level")
	}
}

// TestNewJSONFormat verifies New accepts json format without error.
func TestNewJSONFormat(t *testing.T) {
	cfg := telemetry.Config{LogLevel: "debug", LogFormat: "json"}
	logger, _, err := telemetry.New(cfg)
	if err != nil {
		t.Fatalf("New(json) error: %v", err)
	}
	if logger == nil {
		t.Fatal("logger is nil")
	}
}

// TestRedactingHandlerDenySet verifies AC-7: deny-set attrs are masked in
// rendered output.
var redactTests = []struct {
	key      string
	value    string
	redacted bool
}{
	{"api_key", "sk-supersecret", true},
	{"authorization", "Bearer token123", true},
	{"dsn", "postgres://user:pass@host/db", true},
	{"secret", "my-secret", true},
	{"username", "alice", false},
	{"tenant", "acme", false},
	{"message", "hello world", false},
}

func TestRedactingHandlerDenySet(t *testing.T) {
	for _, tt := range redactTests {
		tt := tt
		t.Run(tt.key, func(t *testing.T) {
			var buf bytes.Buffer
			base := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
			handler := telemetry.NewRedactingHandler(base, telemetry.DefaultDenySet())
			logger := slog.New(handler)

			logger.Info("test", tt.key, tt.value)
			out := buf.String()

			if tt.redacted {
				if strings.Contains(out, tt.value) {
					t.Errorf("key %q: value %q should be redacted but appears in: %s", tt.key, tt.value, out)
				}
				if !strings.Contains(out, "[REDACTED]") {
					t.Errorf("key %q: expected [REDACTED] in output: %s", tt.key, out)
				}
			} else {
				if !strings.Contains(out, tt.value) {
					t.Errorf("key %q: value %q should appear in output: %s", tt.key, tt.value, out)
				}
			}
		})
	}
}

// TestRedactingHandlerWithAttrs verifies deny-set attrs are masked when
// passed via WithAttrs.
func TestRedactingHandlerWithAttrs(t *testing.T) {
	var buf bytes.Buffer
	base := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	handler := telemetry.NewRedactingHandler(base, telemetry.DefaultDenySet())
	logger := slog.New(handler).With("api_key", "sk-should-not-appear")

	logger.Info("event")
	out := buf.String()
	if strings.Contains(out, "sk-should-not-appear") {
		t.Errorf("WithAttrs: secret value should not appear in: %s", out)
	}
	if !strings.Contains(out, "[REDACTED]") {
		t.Errorf("WithAttrs: [REDACTED] not found in: %s", out)
	}
}

// TestWith verifies With annotates the logger with scope attrs.
func TestWith(t *testing.T) {
	var buf bytes.Buffer
	base := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	handler := telemetry.NewRedactingHandler(base, telemetry.DefaultDenySet())
	logger := slog.New(handler)

	scope := identity.Scope{Tenant: "acme", Project: "myproj", User: "u1"}
	ctx := identity.WithScope(context.Background(), scope)

	stamped := telemetry.With(ctx, logger)
	stamped.Info("hello")

	out := buf.String()
	for _, want := range []string{"acme", "myproj", "u1"} {
		if !strings.Contains(out, want) {
			t.Errorf("With: %q not found in output: %s", want, out)
		}
	}
}

// TestWithNoScope verifies With returns the logger unchanged when ctx has no scope.
func TestWithNoScope(t *testing.T) {
	var buf bytes.Buffer
	base := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	logger := slog.New(base)

	result := telemetry.With(context.Background(), logger)
	result.Info("no scope")
	// Should not panic and should still log.
	if !strings.Contains(buf.String(), "no scope") {
		t.Error("With(no scope) should still log the message")
	}
}

// TestRedactingHandlerConcurrent verifies safe concurrent use (CLAUDE.md §5).
func TestRedactingHandlerConcurrent(t *testing.T) {
	var buf syncBuffer
	base := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	handler := telemetry.NewRedactingHandler(base, telemetry.DefaultDenySet())
	logger := slog.New(handler)

	const n = 100
	done := make(chan struct{}, n)
	for i := 0; i < n; i++ {
		go func() {
			logger.Info("concurrent", "api_key", "secret", "user", "alice")
			done <- struct{}{}
		}()
	}
	for i := 0; i < n; i++ {
		<-done
	}
}

// syncBuffer is an io.Writer safe for concurrent use.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}
