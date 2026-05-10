// Package null provides a no-op DocsProvider for offline use and as the
// fallback when no documentation backend is configured (PRD §4.3).
//
// The harness uses this provider when the user sets
// `docs_provider = "null"` in their config, or before M2's Context7
// implementation lands. The tutor still runs; it just has no grounding
// for library-specific questions and the system prompt's GROUNDING
// paragraph (PRE_BUILD_ANSWERS §4) tells the model to say so plainly
// rather than invent API signatures.
package null

import (
	"context"
	"fmt"

	"github.com/lernen-edu/lernen/internal/docs"
)

// Provider is a DocsProvider that has nothing to provide. ResolveLibrary
// and QueryDocs return docs.ErrNotConfigured; HealthCheck returns nil
// because the null provider is operating as intended.
type Provider struct{}

// New returns a ready-to-use null provider. There is no configuration
// surface — every instance is identical.
func New() *Provider {
	return &Provider{}
}

// Compile-time guarantee that Provider satisfies docs.Provider.
var _ docs.Provider = (*Provider)(nil)

// ResolveLibrary always returns docs.ErrNotConfigured wrapped with the
// requested name so logs and tutor warnings can show what was missed.
func (p *Provider) ResolveLibrary(_ context.Context, name string) (docs.LibraryID, error) {
	return "", fmt.Errorf("null docs provider cannot resolve %q: %w", name, docs.ErrNotConfigured)
}

// QueryDocs always returns docs.ErrNotConfigured. The empty string is
// returned for the body so callers that ignore the error still get a
// safe zero value to inject (which they should not do — they should
// branch on the error and fall through to model knowledge per
// PRE_BUILD_ANSWERS §10).
func (p *Provider) QueryDocs(_ context.Context, lib docs.LibraryID, topic string, _ int) (string, error) {
	return "", fmt.Errorf("null docs provider cannot query %q in %q: %w", topic, lib, docs.ErrNotConfigured)
}

// HealthCheck always succeeds. A null provider is healthy whenever it
// exists; the user has either explicitly chosen offline operation or
// has not yet configured a real provider, and in both cases the harness
// should boot without warnings.
func (p *Provider) HealthCheck(_ context.Context) error {
	return nil
}
