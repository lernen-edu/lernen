package languages

import (
	"context"
	"fmt"
	"sort"
	"sync"
)

// registry holds every LanguageAdapter that has been registered. Adapters
// register themselves at compile time via init() in their own packages —
// see internal/languages/python.
var (
	registry   = map[string]LanguageAdapter{}
	registryMu sync.RWMutex
)

// Register adds adapter to the registry under adapter.ID(). Panics on a
// duplicate ID — that's a programming error caught at process start, not a
// runtime condition. Per AGENTS.md, init() is permitted for adapter
// registration; this function is the intended target.
func Register(adapter LanguageAdapter) {
	if adapter == nil {
		panic("languages.Register: nil adapter")
	}
	id := adapter.ID()
	if id == "" {
		panic("languages.Register: adapter has empty ID")
	}

	registryMu.Lock()
	defer registryMu.Unlock()
	if _, exists := registry[id]; exists {
		panic(fmt.Sprintf("languages.Register: %q already registered", id))
	}
	registry[id] = adapter
}

// Get returns the adapter for id and reports whether one is registered.
// Lookup is read-only and safe for concurrent use.
func Get(id string) (LanguageAdapter, bool) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	a, ok := registry[id]
	return a, ok
}

// IDs returns every registered adapter ID, sorted for stable output (useful
// for `lernen setup` listings and tests that compare against a fixed set).
func IDs() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	out := make([]string, 0, len(registry))
	for id := range registry {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// ---- "Unimplemented" stubs ----
//
// Stub adapters in M1 return these for TestRunner / BuildRunner so callers
// don't have to nil-check. M3 replaces them with real implementations.

// UnimplementedTestRunner returns an "M1 stub" error from Run.
type UnimplementedTestRunner struct {
	LanguageID string
}

// Run reports an error indicating the adapter has not been fully implemented.
func (u UnimplementedTestRunner) Run(_ context.Context, _ string) (TestResult, error) {
	return TestResult{}, fmt.Errorf("languages: %s test runner not implemented in M1", u.LanguageID)
}

// UnimplementedBuildRunner returns an "M1 stub" error from Build.
type UnimplementedBuildRunner struct {
	LanguageID string
}

// Build reports an error indicating the adapter has not been fully implemented.
func (u UnimplementedBuildRunner) Build(_ context.Context, _ string) error {
	return fmt.Errorf("languages: %s build runner not implemented in M1", u.LanguageID)
}
