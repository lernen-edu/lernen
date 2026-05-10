package goals

import (
	_ "embed"
	"strings"
)

//go:embed goals.md
var stage0PromptRaw string

//go:embed structurer.md
var structurerPromptRaw string

// Stage0SystemPrompt returns the demanding-mentor goal-elicitation
// system prompt for Stage 0 of the forge. It is embedded from
// goals.md at compile time. The trailing newline is trimmed so the
// rendered prompt threads cleanly into Backend.Chat message bodies.
func Stage0SystemPrompt() string {
	return strings.TrimRight(stage0PromptRaw, "\n")
}

// StructurerSystemPrompt returns the system prompt used for the
// non-streaming call-2 structuring step. It is embedded from
// structurer.md at compile time and contains the YAML schema inline.
func StructurerSystemPrompt() string {
	return strings.TrimRight(structurerPromptRaw, "\n")
}
