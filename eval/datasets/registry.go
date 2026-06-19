package datasets

import (
	"context"
	"fmt"
	"io"
	"sort"
	"sync"
)

// Spec describes a registered eval dataset: where its raw file lives, how to
// fetch it, and how to normalize its wire format into the common
// Conversation/Question shape. A dataset registers itself in an init() (the
// runner and CLI blank-import the subpackages), so adding a benchmark is a new
// subpackage + one Register call — there is no central switch to edit. This is
// the factory the public-benchmark runner selects through (bar-remediation #7,
// D-096): longmemeval, longmemeval_s, and locomo all resolve here and flow
// through the single harness.RunDataset path.
type Spec struct {
	// Name is the dataset selector (e.g. "longmemeval", "longmemeval_s", "locomo").
	Name string
	// DataFile is the path, relative to the eval data root, where Fetch writes the
	// raw dataset and where the runner reads it.
	DataFile string
	// Fetch downloads the dataset into dataRoot and returns the written path.
	Fetch func(ctx context.Context, dataRoot string) (string, error)
	// Normalize parses the raw wire format into the common shape.
	Normalize func(r io.Reader) ([]Conversation, []Question, error)
}

var (
	regMu    sync.RWMutex
	registry = map[string]Spec{}
)

// Register adds a dataset spec. Called from a dataset subpackage's init().
// Panics on an empty name or a duplicate registration (a programming error
// surfaced at startup, never at runtime).
func Register(s Spec) {
	if s.Name == "" {
		panic("datasets: Register with empty Name")
	}
	if s.Normalize == nil || s.Fetch == nil {
		panic("datasets: Register " + s.Name + " missing Fetch/Normalize")
	}
	regMu.Lock()
	defer regMu.Unlock()
	if _, dup := registry[s.Name]; dup {
		panic("datasets: duplicate registration for " + s.Name)
	}
	registry[s.Name] = s
}

// Lookup returns the spec for name, or ok=false when unregistered.
func Lookup(name string) (Spec, bool) {
	regMu.RLock()
	defer regMu.RUnlock()
	s, ok := registry[name]
	return s, ok
}

// Names returns the registered dataset names, sorted.
func Names() []string {
	regMu.RLock()
	defer regMu.RUnlock()
	out := make([]string, 0, len(registry))
	for n := range registry {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// MustLookup returns the spec for name or an error listing the known datasets.
func MustLookup(name string) (Spec, error) {
	if s, ok := Lookup(name); ok {
		return s, nil
	}
	return Spec{}, fmt.Errorf("unknown dataset %q (known: %v)", name, Names())
}
