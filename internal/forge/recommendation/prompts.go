package recommendation

import (
	"bytes"
	_ "embed"
	"strings"
	"text/template"

	"github.com/lernen-edu/lernen/internal/forge/calibration"
	"github.com/lernen-edu/lernen/internal/forge/goals"
)

//go:embed recommendation.md
var stage2PromptRaw string

//go:embed structurer.md
var structurerPromptRaw string

// stage2Tmpl is parsed once at package init. If recommendation.md
// references a placeholder not in the data struct used by
// Stage2SystemPrompt, this panics at startup — the failure surfaces
// in tests / on first command invocation, not silently.
var stage2Tmpl = template.Must(template.New("stage2").Parse(stage2PromptRaw))

// structurerTmpl is parsed once at package init. The structurer.md
// template uses `{{.AdapterIDs}}` to render the valid-language set
// inline; the Go template engine renders ranges over string slices
// natively.
var structurerTmpl = template.Must(template.New("structurer").Parse(structurerPromptRaw))

// Stage2SystemPrompt returns the recommendation system prompt with
// the user's goals, calibration starting-point, and the registered-
// adapter list interpolated in. Whitespace around each goals/
// starting-point input is trimmed so the rendered prompt never has
// lopsided indentation around the blockquote markers in the template.
//
// Returns the prompt with trailing newline trimmed so it threads
// cleanly into Backend.Chat message bodies.
func Stage2SystemPrompt(g *goals.Goals, sp *calibration.StartingPoint, adapters []AdapterInfo) string {
	data := struct {
		TargetCapability string
		TargetProject    string
		CurrentModel     string
		Gaps             string
		PriorLanguages   string
		Adapters         []AdapterInfo
	}{
		TargetCapability: strings.TrimSpace(g.TargetCapability),
		TargetProject:    strings.TrimSpace(g.TargetProject),
		CurrentModel:     strings.TrimSpace(sp.CurrentModel),
		Gaps:             strings.TrimSpace(sp.Gaps),
		PriorLanguages:   strings.TrimSpace(sp.PriorLanguages),
		Adapters:         adapters,
	}
	var buf bytes.Buffer
	if err := stage2Tmpl.Execute(&buf, data); err != nil {
		panic("recommendation: render Stage2SystemPrompt: " + err.Error())
	}
	return strings.TrimRight(buf.String(), "\n")
}

// StructurerSystemPrompt returns the system prompt used for the
// non-streaming call-2 structuring step, with the valid adapter ID
// set rendered inline so the model knows the bounded language choice.
//
// Returns the prompt with trailing newline trimmed.
func StructurerSystemPrompt(adapterIDs []string) string {
	data := struct {
		AdapterIDs []string
	}{
		AdapterIDs: adapterIDs,
	}
	var buf bytes.Buffer
	if err := structurerTmpl.Execute(&buf, data); err != nil {
		panic("recommendation: render StructurerSystemPrompt: " + err.Error())
	}
	return strings.TrimRight(buf.String(), "\n")
}
