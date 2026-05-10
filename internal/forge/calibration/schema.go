// Package calibration defines the M3b Stage 1 output schema. Mirrors
// internal/forge/goals (Stage 0): same harness shape, same prose-only
// schema philosophy. Later tasks in this package add the structuring
// call that produces a StartingPoint.
package calibration

import (
	"fmt"
	"strings"
	"time"
)

// CurrentSchemaVersion is the schema_version M3b writes and validates.
// Mirrors goals.CurrentSchemaVersion: bumps reserved for breaking
// changes; additive optional fields populated by later stages do not bump.
const CurrentSchemaVersion = 1

// StartingPoint is the structured output of forge Stage 1. It captures
// the user's current model of programming, their gaps relative to the
// goals.yaml target, and the languages they have any prior exposure
// to. Downstream stages (M3c recommendation onward) bind to these
// fields by name.
//
// All four content fields are block-scalar strings rather than
// structured shapes, mirroring the M3a Goals.Notes migration: list
// items repeatedly tripped yaml.v3 (quoted-then-prose, unquoted ":")
// and a free-prose block sidesteps the entire failure class. M3c can
// extract structured language signal from prior_languages prose if it
// needs to.
type StartingPoint struct {
	SchemaVersion     int       `yaml:"schema_version"`
	AuthoredAt        time.Time `yaml:"authored_at"`
	CurrentModel      string    `yaml:"current_model"`
	Gaps              string    `yaml:"gaps"`
	PriorLanguages    string    `yaml:"prior_languages"`
	ForgeVoiceSummary string    `yaml:"forge_voice_summary"`
}

// Validate enforces the M3b contract:
//   - schema_version equals CurrentSchemaVersion
//   - authored_at is non-zero
//   - all four content fields are non-empty after trimming whitespace
//
// No optional fields in this version — calibration must produce signal
// on all four content fields.
func (sp *StartingPoint) Validate() error {
	if sp.SchemaVersion != CurrentSchemaVersion {
		return fmt.Errorf("starting_point: schema_version %d unsupported; want %d", sp.SchemaVersion, CurrentSchemaVersion)
	}
	if sp.AuthoredAt.IsZero() {
		return fmt.Errorf("starting_point: authored_at is zero")
	}
	required := []struct {
		name  string
		value string
	}{
		{"current_model", sp.CurrentModel},
		{"gaps", sp.Gaps},
		{"prior_languages", sp.PriorLanguages},
		{"forge_voice_summary", sp.ForgeVoiceSummary},
	}
	for _, f := range required {
		if strings.TrimSpace(f.value) == "" {
			return fmt.Errorf("starting_point: %s is empty", f.name)
		}
	}
	return nil
}
