package phase1

import (
	_ "embed"
	"strings"
)

// phase1TutorRaw is the literal demanding-mentor system prompt for Phase 1
// tutor turns, embedded from phase1_tutor.md.
//
// The text is locked in docs/PRE_BUILD_ANSWERS.md §1. If you change the .md
// file here, propose the corresponding change in PRE_BUILD_ANSWERS.md so the
// project's spec and the shipped binary stay in sync.
//
//go:embed phase1_tutor.md
var phase1TutorRaw string

// Phase1TutorSystemPrompt is the demanding-mentor system prompt the harness
// sends to the backend at the start of every tutor turn.
//
// The prompt contains two slots that RenderPhase1SystemPrompt fills at session
// start: {{language_display_name}} and {{chapter_title}}. The rest of the
// prompt is constant.
var Phase1TutorSystemPrompt = strings.TrimRight(phase1TutorRaw, "\n")

// RenderPhase1SystemPrompt fills the {{language_display_name}} and
// {{chapter_title}} slots in Phase1TutorSystemPrompt, then appends the
// language adapter's addendum (if non-empty), separated from the main prompt
// by one blank line.
//
// Callers are responsible for ensuring languageDisplayName and chapterTitle
// are non-empty; render does not invent placeholder text.
func RenderPhase1SystemPrompt(languageDisplayName, chapterTitle, adapterAddendum string) string {
	out := strings.ReplaceAll(Phase1TutorSystemPrompt, "{{language_display_name}}", languageDisplayName)
	out = strings.ReplaceAll(out, "{{chapter_title}}", chapterTitle)
	if adapterAddendum != "" {
		out = out + "\n\n" + strings.TrimRight(adapterAddendum, "\n")
	}
	return out
}
