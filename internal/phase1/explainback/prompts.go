package explainback

import (
	_ "embed"
)

//go:embed evaluator.md
var evaluatorPrompt string

// SystemPrompt returns the fixed gate system prompt. It does not
// interpolate — the pending turn and transcript window are passed
// through the Chat user message instead.
func SystemPrompt() string {
	return evaluatorPrompt
}
