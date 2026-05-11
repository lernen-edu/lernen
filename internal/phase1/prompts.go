package phase1

import (
	_ "embed"
	"fmt"
	"strings"

	"github.com/lernen-edu/lernen/internal/curriculum"
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
// The prompt contains three slots that RenderPhase1SystemPrompt fills at
// session start: {{language_display_name}}, {{chapter_title}}, and
// {{chapter_context}}. The rest of the prompt is constant.
var Phase1TutorSystemPrompt = strings.TrimRight(phase1TutorRaw, "\n")

// RenderPhase1SystemPrompt fills the {{language_display_name}},
// {{chapter_title}}, and {{chapter_context}} slots in
// Phase1TutorSystemPrompt, then appends the language adapter's addendum
// (if non-empty), separated from the main prompt by one blank line.
//
// The chapter_context section lists the chapter's introduced competencies
// (with tiers + descriptions) and authored exercises. For orientation
// chapters (no CompetenciesIntroduced) it emits a short note instead.
//
// Callers are responsible for ensuring languageDisplayName is non-empty
// and chapter is non-nil; render does not invent placeholder text.
func RenderPhase1SystemPrompt(languageDisplayName string, chapter *curriculum.Chapter, comps []curriculum.Competency, adapterAddendum string) string {
	out := strings.ReplaceAll(Phase1TutorSystemPrompt, "{{language_display_name}}", languageDisplayName)
	chapterTitle := ""
	if chapter != nil {
		chapterTitle = chapter.Title
	}
	out = strings.ReplaceAll(out, "{{chapter_title}}", chapterTitle)
	out = strings.ReplaceAll(out, "{{chapter_context}}", renderChapterContext(chapter, comps))
	if adapterAddendum != "" {
		out = out + "\n\n" + strings.TrimRight(adapterAddendum, "\n")
	}
	return out
}

// renderChapterContext emits the bullet list of introduced competencies
// (with tier + description) and authored exercises for the chapter.
// Designed to be paraphrasable by the mentor at every turn.
func renderChapterContext(chapter *curriculum.Chapter, comps []curriculum.Competency) string {
	if chapter == nil {
		return "(no chapter context available)"
	}
	var b strings.Builder
	if len(chapter.CompetenciesIntroduced) > 0 {
		b.WriteString("Competencies introduced in this chapter:\n")
		introduced := make(map[string]struct{}, len(chapter.CompetenciesIntroduced))
		for _, id := range chapter.CompetenciesIntroduced {
			introduced[id] = struct{}{}
		}
		for _, c := range comps {
			if _, ok := introduced[c.ID]; !ok {
				continue
			}
			fmt.Fprintf(&b, "  - %s (%s) — %s\n", c.ID, c.Tier, oneLine(c.Description))
		}
	} else {
		b.WriteString("This is an orientation chapter — no drillable competencies. The learner should be able to articulate the chapter's explain-back target in their own words.\n")
	}
	if len(chapter.Exercises) > 0 {
		b.WriteString("\nAuthored exercises (use as anchors; demonstrations can also happen organically in dialogue):\n")
		for _, ex := range chapter.Exercises {
			fmt.Fprintf(&b, "  - %s: %s\n", ex.ID, oneLine(ex.Prompt))
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
