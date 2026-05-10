package ingestion

import (
	_ "embed"
	"fmt"
	"strings"
	"text/template"

	"github.com/lernen-edu/lernen/internal/forge/calibration"
	"github.com/lernen-edu/lernen/internal/forge/goals"
	"github.com/lernen-edu/lernen/internal/forge/recommendation"
)

//go:embed ingestion.md
var stage3SystemPromptTmpl string

//go:embed structurer.md
var structurerSystemPromptText string

var stage3Tmpl = template.Must(template.New("stage3").Parse(stage3SystemPromptTmpl))

// Stage3SystemPrompt renders the Stage 3 mentor system prompt with
// the three prior YAMLs interpolated. Panics on any nil pointer
// (programmer error: orchestrator must load all three before
// dispatching ingestion.Run).
func Stage3SystemPrompt(g *goals.Goals, sp *calibration.StartingPoint, rec *recommendation.Recommendation) string {
	if g == nil {
		panic("ingestion: Stage3SystemPrompt: goals is nil")
	}
	if sp == nil {
		panic("ingestion: Stage3SystemPrompt: starting_point is nil")
	}
	if rec == nil {
		panic("ingestion: Stage3SystemPrompt: recommendation is nil")
	}
	data := struct {
		TargetCapability string
		TargetProject    string
		CurrentModel     string
		Gaps             string
		PriorLanguages   string
		Language         string
		CurriculumName   string
		CurriculumSource string
		Rationale        string
	}{
		TargetCapability: strings.TrimSpace(g.TargetCapability),
		TargetProject:    strings.TrimSpace(g.TargetProject),
		CurrentModel:     strings.TrimSpace(sp.CurrentModel),
		Gaps:             strings.TrimSpace(sp.Gaps),
		PriorLanguages:   strings.TrimSpace(sp.PriorLanguages),
		Language:         strings.TrimSpace(rec.Language),
		CurriculumName:   strings.TrimSpace(rec.CurriculumName),
		CurriculumSource: strings.TrimSpace(rec.CurriculumSource),
		Rationale:        strings.TrimSpace(rec.Rationale),
	}
	var sb strings.Builder
	if err := stage3Tmpl.Execute(&sb, data); err != nil {
		panic(fmt.Sprintf("ingestion: Stage3SystemPrompt template execute: %v", err))
	}
	return strings.TrimRight(sb.String(), "\n")
}

// StructurerSystemPrompt returns the call-2 prompt that converts the
// Stage 3 transcript into ingestion.yaml. No interpolation — the
// adapter set is irrelevant here (Stage 3 doesn't pick a language)
// and the curriculum name is read from the transcript.
func StructurerSystemPrompt() string {
	return strings.TrimRight(structurerSystemPromptText, "\n")
}
