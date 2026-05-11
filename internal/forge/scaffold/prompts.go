package scaffold

import (
	"bytes"
	_ "embed"
	"fmt"
	"strings"
	"text/template"

	"github.com/lernen-edu/lernen/internal/forge/calibration"
	"github.com/lernen-edu/lernen/internal/forge/goals"
	"github.com/lernen-edu/lernen/internal/forge/ingestion"
	"github.com/lernen-edu/lernen/internal/forge/recommendation"
)

//go:embed classify.md
var classifyPromptRaw string

//go:embed classify_structurer.md
var classifyStructurerPromptRaw string

var classifyTmpl = template.Must(template.New("classify").Parse(classifyPromptRaw))
var classifyStructurerTmpl = template.Must(template.New("classify_structurer").Parse(classifyStructurerPromptRaw))

// Pass1SystemPrompt renders the Pass 1 mentor system prompt with the
// four prior-stage outputs interpolated. Whitespace around free-prose
// fields is trimmed so the rendered prompt has clean formatting.
func Pass1SystemPrompt(g *goals.Goals, sp *calibration.StartingPoint, rec *recommendation.Recommendation, ing *ingestion.Ingestion) string {
	data := struct {
		TargetCapability string
		TargetProject    string
		CurrentModel     string
		Gaps             string
		PriorLanguages   string
		Language         string
		CurriculumName   string
		CurriculumSource string
		Chapters         []ingestion.Chapter
	}{
		TargetCapability: strings.TrimSpace(g.TargetCapability),
		TargetProject:    strings.TrimSpace(g.TargetProject),
		CurrentModel:     strings.TrimSpace(sp.CurrentModel),
		Gaps:             strings.TrimSpace(sp.Gaps),
		PriorLanguages:   strings.TrimSpace(sp.PriorLanguages),
		Language:         strings.TrimSpace(rec.Language),
		CurriculumName:   strings.TrimSpace(rec.CurriculumName),
		CurriculumSource: strings.TrimSpace(rec.CurriculumSource),
		Chapters:         ing.Chapters,
	}
	var buf bytes.Buffer
	if err := classifyTmpl.Execute(&buf, data); err != nil {
		panic("scaffold: render Pass1SystemPrompt: " + err.Error())
	}
	return strings.TrimRight(buf.String(), "\n")
}

// ClassifyStructurerSystemPrompt renders the Pass 1 structurer prompt
// with the chapter id set inline so the model knows the exact required
// output shape.
func ClassifyStructurerSystemPrompt(chapterIDs []string) string {
	data := struct {
		ChapterIDs []string
	}{
		ChapterIDs: chapterIDs,
	}
	var buf bytes.Buffer
	if err := classifyStructurerTmpl.Execute(&buf, data); err != nil {
		panic("scaffold: render ClassifyStructurerSystemPrompt: " + err.Error())
	}
	return strings.TrimRight(buf.String(), "\n")
}

//go:embed scaffold.md
var scaffoldPromptRaw string

//go:embed scaffold_structurer.md
var scaffoldStructurerPromptRaw string

var scaffoldTmpl = template.Must(template.New("scaffold").Parse(scaffoldPromptRaw))
var scaffoldStructurerTmpl = template.Must(template.New("scaffold_structurer").Parse(scaffoldStructurerPromptRaw))

// Pass2SystemPrompt renders the Pass 2 mentor system prompt for the
// whole pass. The prompt lists all unscaffolded chapters in order and
// instructs the mentor to walk them one at a time, with /next between.
// Per-chapter transitions are announced via Pass2ChapterAnnouncement,
// which the orchestrator emits as a tui.ContextMsg.
func Pass2SystemPrompt(g *goals.Goals, sp *calibration.StartingPoint, rec *recommendation.Recommendation, ing *ingestion.Ingestion, cc *ClassifiedChapters, startIdx int) string {
	titleByID := make(map[string]string, len(ing.Chapters))
	for _, c := range ing.Chapters {
		titleByID[c.ID] = c.Title
	}
	type chapterRow struct {
		ID    string
		Title string
		Kind  string
	}
	rows := make([]chapterRow, 0, len(cc.Classifications)-startIdx)
	for i := startIdx; i < len(cc.Classifications); i++ {
		cl := cc.Classifications[i]
		rows = append(rows, chapterRow{ID: cl.ChapterID, Title: titleByID[cl.ChapterID], Kind: cl.Kind})
	}
	data := struct {
		TargetCapability string
		TargetProject    string
		CurrentModel     string
		Gaps             string
		Language         string
		CurriculumName   string
		Chapters         []chapterRow
	}{
		TargetCapability: strings.TrimSpace(g.TargetCapability),
		TargetProject:    strings.TrimSpace(g.TargetProject),
		CurrentModel:     strings.TrimSpace(sp.CurrentModel),
		Gaps:             strings.TrimSpace(sp.Gaps),
		Language:         strings.TrimSpace(rec.Language),
		CurriculumName:   strings.TrimSpace(rec.CurriculumName),
		Chapters:         rows,
	}
	var buf bytes.Buffer
	if err := scaffoldTmpl.Execute(&buf, data); err != nil {
		panic("scaffold: render Pass2SystemPrompt: " + err.Error())
	}
	return strings.TrimRight(buf.String(), "\n")
}

// Pass2ChapterAnnouncement returns the directive context message
// announced at each chapter transition during Pass 2. The orchestrator
// emits this as the Text of a tui.ContextMsg with AutoReply=true so
// the mentor immediately reacts and pivots without waiting for the
// user to prompt.
//
// Format: starts with "TRANSITION:" so the model recognizes a context
// shift; contains the chapter id verbatim (load-bearing for
// extractSubTranscript's anchor); explicitly tells the mentor what to
// do and what NOT to do to counter anchoring on prior /next reminders.
func Pass2ChapterAnnouncement(idx int, chapterID, title, kind string) string {
	return fmt.Sprintf(
		"TRANSITION: User has committed the previous chapter's scaffold and pressed /next. "+
			"Begin chapter %d now: `%s` — %s (kind: %s). "+
			"Your next response MUST be the opening of this chapter — propose a competency or ask the explain-back question, depending on kind. "+
			"Do NOT ask the user to /next (they already did). "+
			"Do NOT summarize the previous chapter. "+
			"Do NOT acknowledge or apologize for confusion. "+
			"Just open the chapter.",
		idx+1, chapterID, title, kind,
	)
}

// ScaffoldStructurerSystemPrompt renders the Pass 2 structurer prompt
// for one chapter. The kind is interpolated so the model knows whether
// to emit an orientation shape or a content shape.
func ScaffoldStructurerSystemPrompt(chapterID, kind string) string {
	data := struct {
		ChapterID string
		Kind      string
	}{
		ChapterID: chapterID,
		Kind:      kind,
	}
	var buf bytes.Buffer
	if err := scaffoldStructurerTmpl.Execute(&buf, data); err != nil {
		panic("scaffold: render ScaffoldStructurerSystemPrompt: " + err.Error())
	}
	return strings.TrimRight(buf.String(), "\n")
}
