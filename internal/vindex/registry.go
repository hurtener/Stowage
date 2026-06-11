package vindex

import (
	"fmt"
	"sync"

	"github.com/hurtener/stowage/internal/config"
	"github.com/hurtener/stowage/internal/store"
)

// Factory is the constructor signature all vindex drivers must provide.
// Drivers register themselves via Register from their init() function.
type Factory func(vs store.VectorStore, dims int, model string) (Index, error)

var (
	regMu    sync.RWMutex
	registry = map[string]Factory{}
)

// Register associates a driver name with its factory.
// Drivers call this from their init() function (blank-import activation).
func Register(driver string, f Factory) {
	regMu.Lock()
	defer regMu.Unlock()
	registry[driver] = f
}

// Open looks up the driver registered for cfg.Driver and calls its factory.
// Returns an error when no factory has been registered for the driver name.
func Open(cfg config.VIndexConfig, vs store.VectorStore, dims int, model string) (Index, error) {
	regMu.RLock()
	f, ok := registry[cfg.Driver]
	regMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("vindex: unknown driver %q (not registered; missing blank import?)", cfg.Driver)
	}
	return f(vs, dims, model)
}

func init() {
	// Register the brute-force driver under the name "brute".
	// The brute driver is the exact-recall oracle and the v1 baseline (D-046).
	Register("brute", func(vs store.VectorStore, dims int, model string) (Index, error) {
		return New(vs, dims, model), nil
	})
}
