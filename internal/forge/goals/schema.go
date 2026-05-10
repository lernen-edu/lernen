// Package goals defines the M3a Stage 0 output schema and the
// structuring call that produces it.
package goals

import (
	"fmt"
	"strings"
	"time"
)

// CurrentSchemaVersion is the schema_version M3a writes and validates.
// M3b and later may extend the struct without bumping; version bumps
// are reserved for breaking changes.
const CurrentSchemaVersion = 1

// Goals is the structured output of forge Stage 0. It captures the
// learner's articulated goals, motivation, prior history, success
// definition, and target project, plus a free-form notes block for
// observations that don't fit a tight field. Downstream stages bind
// to the tight fields by name; notes catches what the conversation
// surfaces that the schema didn't anticipate.
//
// Notes is a single block-scalar string rather than a list. The list
// shape was tried in early M3a dogfooding and the model kept emitting
// list items that yaml.v3 rejected (quoted-then-prose, unquoted ":"),
// each requiring a salvage rule. A free-prose block sidesteps the
// entire failure class and reads more naturally anyway.
type Goals struct {
	SchemaVersion     int       `yaml:"schema_version"`
	AuthoredAt        time.Time `yaml:"authored_at"`
	TargetCapability  string    `yaml:"target_capability"`
	Motivation        string    `yaml:"motivation"`
	PriorAttempts     string    `yaml:"prior_attempts"`
	SuccessDefinition string    `yaml:"success_definition"`
	TargetProject     string    `yaml:"target_project"`
	Notes             string    `yaml:"notes,omitempty"`
	ForgeVoiceSummary string    `yaml:"forge_voice_summary"`
}

// Validate enforces the M3a contract:
//   - schema_version equals CurrentSchemaVersion
//   - authored_at is non-zero
//   - the five tight prompt fields and forge_voice_summary are all
//     non-empty (after trimming whitespace)
//   - notes may be empty or absent (free-form prose, no shape)
func (g *Goals) Validate() error {
	if g.SchemaVersion != CurrentSchemaVersion {
		return fmt.Errorf("goals: schema_version %d unsupported; want %d", g.SchemaVersion, CurrentSchemaVersion)
	}
	if g.AuthoredAt.IsZero() {
		return fmt.Errorf("goals: authored_at is zero")
	}
	required := []struct {
		name  string
		value string
	}{
		{"target_capability", g.TargetCapability},
		{"motivation", g.Motivation},
		{"prior_attempts", g.PriorAttempts},
		{"success_definition", g.SuccessDefinition},
		{"target_project", g.TargetProject},
		{"forge_voice_summary", g.ForgeVoiceSummary},
	}
	for _, f := range required {
		if strings.TrimSpace(f.value) == "" {
			return fmt.Errorf("goals: %s is empty", f.name)
		}
	}
	return nil
}
