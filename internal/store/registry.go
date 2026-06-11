package store

import (
	"context"
	"fmt"
	"sync"

	"github.com/hurtener/stowage/internal/config"
)

// Factory is the constructor signature all drivers must provide.
type Factory func(ctx context.Context, cfg config.StoreConfig) (Store, error)

var (
	mu       sync.RWMutex
	registry = map[string]Factory{}
)

// Register associates a driver name with its factory. Drivers call this from
// their init() function.
func Register(driver string, f Factory) {
	mu.Lock()
	defer mu.Unlock()
	registry[driver] = f
}

// Open looks up the driver registered for cfg.Driver and calls its factory.
// Returns ErrDriverNotRegistered (wrapped) when no factory is found.
func Open(ctx context.Context, cfg config.StoreConfig) (Store, error) {
	mu.RLock()
	f, ok := registry[cfg.Driver]
	mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrDriverNotRegistered, cfg.Driver)
	}
	return f(ctx, cfg)
}
