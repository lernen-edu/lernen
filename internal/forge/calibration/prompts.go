package calibration

import (
	"bytes"
	_ "embed"
	"strings"
	"text/template"

	"github.com/lernen-edu/lernen/internal/forge/goals"
)

//go:embed calibration.md
var stage1PromptRaw string

//go:embed structurer.md
var structurerPromptRaw string

// stage1Tmpl is parsed once at package init. If calibration.md
// references a placeholder not in the data struct used by
// Stage1SystemPrompt, this panics at startup — the failure surfaces
// in tests / on first command invocation, not silently.
var stage1Tmpl = template.Must(template.New("stage1").Parse(stage1PromptRaw))

// Stage1SystemPrompt returns the calibration system prompt with the
// user's target_capability and target_project from goals.yaml
// interpolated in. Whitespace around each input is trimmed so the
// rendered prompt never has lopsided indentation around the
// blockquote markers in the template.
//
// Returns the prompt with trailing newline trimmed so it threads
// cleanly into Backend.Chat message bodies.
func Stage1SystemPrompt(g *goals.Goals) string {
	data := struct {
		TargetCapability string
		TargetProject    string
	}{
		TargetCapability: strings.TrimSpace(g.TargetCapability),
		TargetProject:    strings.TrimSpace(g.TargetProject),
	}
	var buf bytes.Buffer
	if err := stage1Tmpl.Execute(&buf, data); err != nil {
		// Template was parsed at init; an Execute failure here means
		// the template references a field not in the struct above —
		// a programmer error that should never reach production.
		panic("calibration: render Stage1SystemPrompt: " + err.Error())
	}
	return strings.TrimRight(buf.String(), "\n")
}

// StructurerSystemPrompt returns the system prompt used for the
// non-streaming call-2 structuring step. It is embedded from
// structurer.md at compile time and contains the YAML schema inline.
func StructurerSystemPrompt() string {
	return strings.TrimRight(structurerPromptRaw, "\n")
}
