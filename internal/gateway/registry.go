package gateway

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/hurtener/stowage/internal/config"
	"github.com/prometheus/client_golang/prometheus"
)

// Factory is the constructor signature all gateway drivers must provide.
// Drivers register themselves in their init() functions via Register.
type Factory func(
	ctx context.Context,
	cfg config.GatewayConfig,
	log *slog.Logger,
	reg *prometheus.Registry,
) (Gateway, error)

var (
	mu  sync.RWMutex
	reg = map[string]Factory{}
)

// Register associates a driver name with its factory.
// Drivers call this from their init() function (blank-import activation).
func Register(driver string, f Factory) {
	mu.Lock()
	defer mu.Unlock()
	reg[driver] = f
}

// Open looks up the driver registered for cfg.Driver and calls its factory.
// Returns ErrDriverNotRegistered (wrapped) when no factory has been registered.
func Open(
	ctx context.Context,
	cfg config.GatewayConfig,
	log *slog.Logger,
	prom *prometheus.Registry,
) (Gateway, error) {
	mu.RLock()
	f, ok := reg[cfg.Driver]
	mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrDriverNotRegistered, cfg.Driver)
	}
	return f(ctx, cfg, log, prom)
}
