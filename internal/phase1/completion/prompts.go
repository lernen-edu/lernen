// Package completion implements the Phase 1 chapter-completion
// structurer: when the user types /next, the harness dispatches one
// non-streaming Chat call against this prompt; the structurer emits a
// typed progress.ChapterCompletion that the orchestrator persists.
//
// Mirrors internal/forge/reflection/structure.go in shape — same
// retry-once pattern, same closure-state-after-quit flow at the call
// site.
package completion

import (
	_ "embed"
)

//go:embed structurer.md
var structurerPrompt string

// StructurerSystemPrompt returns the fixed structurer system prompt.
// It does not interpolate — chapter context is passed through the
// Chat user message instead.
func StructurerSystemPrompt() string {
	return structurerPrompt
}
