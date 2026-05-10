// Package docs defines the DocsProvider abstraction used by the tutor and
// forge to ground responses in current library documentation.
//
// In M1 only the Provider interface and the NullProvider implementation
// (subpackage internal/docs/null) ship. The Context7-backed default
// implementation and the SQLite CachingProvider land in M2; see PRD §4.3
// and PRE_BUILD_ANSWERS §10 for the calling pattern.
package docs

import (
	"context"
	"errors"
)

// LibraryID identifies a documentation source. The string carries the
// Context7 "/org/project" or "/org/project/version" form as documented in
// PRD §4.3. M1 does not parse or validate the value; it only passes it
// through. M2's Context7 implementation will be the first reader to care
// about the structure.
type LibraryID string

// ErrNotConfigured is returned when a Provider implementation has no
// upstream documentation source it can reach. The NullProvider returns
// this from ResolveLibrary and QueryDocs so callers can branch on the
// "docs disabled" state with errors.Is rather than string matching.
//
// HealthCheck does not return this error: a Provider that is working as
// intended (including a NullProvider) reports a healthy status.
var ErrNotConfigured = errors.New("docs: documentation provider is not configured")

// Provider is the runtime documentation surface defined in PRD §4.3.
//
// Implementations must be safe for concurrent use. ResolveLibrary and
// QueryDocs must respect context cancellation so the harness can abort a
// fetch when the user hits ESC during a tutor turn.
type Provider interface {
	// ResolveLibrary maps a human library name (e.g. "pytest") to a
	// LibraryID the provider can later look up. Implementations should
	// pick the best match using the same heuristics documented in
	// .claude/rules/context7.md (exact name, description relevance,
	// snippet count, source reputation).
	ResolveLibrary(ctx context.Context, name string) (LibraryID, error)

	// QueryDocs returns documentation text for a topic within lib,
	// trimmed to roughly maxTokens worth of content. The returned string
	// is injected verbatim into a system message before the tutor's turn
	// (PRE_BUILD_ANSWERS §10) so it must already be readable Markdown or
	// plain text — no JSON envelopes, no chrome.
	QueryDocs(ctx context.Context, lib LibraryID, topic string, maxTokens int) (string, error)

	// HealthCheck reports whether the provider can reach its upstream.
	// Returning nil means "ready to serve QueryDocs"; an error means the
	// harness should warn the user and degrade gracefully (PRD §4.3).
	HealthCheck(ctx context.Context) error
}
