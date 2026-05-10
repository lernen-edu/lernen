// Package recommendation defines the M3c Stage 2 output schema. Mirrors
// internal/forge/goals (Stage 0) and internal/forge/calibration
// (Stage 1): same harness shape, same prose-heavy schema philosophy.
// The one structural deviation is the typed `language` field,
// validated against the LanguageAdapter registry — load-bearing for
// downstream stages (M3d ingestion, M3e scaffolding) that bind to a
// specific adapter. Later tasks in this package add the structuring
// call that produces a Recommendation.
package recommendation

import (
	"fmt"
	"strings"
	"time"

	"github.com/lernen-edu/lernen/internal/languages"
)

// CurrentSchemaVersion is the schema_version M3c writes and validates.
// Mirrors goals.CurrentSchemaVersion and calibration.CurrentSchemaVersion:
// bumps reserved for breaking changes; additive optional fields
// populated by later stages do not bump.
const CurrentSchemaVersion = 1

// Recommendation is the structured output of forge Stage 2. It captures
// the language and curriculum the mentor proposed and the user
// accepted, with the reasoning anchored to the prior YAMLs (goals +
// starting_point) and the adapter set Lernen ships.
//
// The Language field is a registered LanguageAdapter ID (e.g.,
// "python"). Validate() checks it against the live registry to fail
// at write-time on typos or hallucinated IDs — a non-existent adapter
// would propagate into M3d ingestion and M3e scaffolding failures.
//
// The other content fields are free-form prose block scalars,
// matching the M3a/M3b schema philosophy.
type Recommendation struct {
	SchemaVersion          int       `yaml:"schema_version"`
	AuthoredAt             time.Time `yaml:"authored_at"`
	Language               string    `yaml:"language"`
	CurriculumName         string    `yaml:"curriculum_name"`
	CurriculumSource       string    `yaml:"curriculum_source"`
	Rationale              string    `yaml:"rationale"`
	AlternativesConsidered string    `yaml:"alternatives_considered"`
	ForgeVoiceSummary      string    `yaml:"forge_voice_summary"`
}

// AdapterInfo is a small DTO carrying a registered language adapter's
// public metadata. The recommendation prompt rendering and the
// structurer prompt rendering both consume this; production callers
// build it from the languages registry, tests inject custom sets.
type AdapterInfo struct {
	ID          string
	DisplayName string
}

// Validate enforces the M3c contract:
//   - schema_version equals CurrentSchemaVersion
//   - authored_at is non-zero
//   - language is non-empty AND present in languages.IDs()
//   - all five content fields are non-empty after trimming whitespace
//
// The language check is what makes M3c's schema bind to the live
// adapter registry. The error message names both the offending ID and
// the registered set so the user (or the structurer call's retry
// logic, if any) can correct.
func (r *Recommendation) Validate() error {
	if r.SchemaVersion != CurrentSchemaVersion {
		return fmt.Errorf("recommendation: schema_version %d unsupported; want %d", r.SchemaVersion, CurrentSchemaVersion)
	}
	if r.AuthoredAt.IsZero() {
		return fmt.Errorf("recommendation: authored_at is zero")
	}
	if strings.TrimSpace(r.Language) == "" {
		return fmt.Errorf("recommendation: language is empty")
	}
	if _, ok := languages.Get(r.Language); !ok {
		return fmt.Errorf("recommendation: language %q is not a registered adapter; registered: %v", r.Language, languages.IDs())
	}
	required := []struct {
		name  string
		value string
	}{
		{"curriculum_name", r.CurriculumName},
		{"curriculum_source", r.CurriculumSource},
		{"rationale", r.Rationale},
		{"alternatives_considered", r.AlternativesConsidered},
		{"forge_voice_summary", r.ForgeVoiceSummary},
	}
	for _, f := range required {
		if strings.TrimSpace(f.value) == "" {
			return fmt.Errorf("recommendation: %s is empty", f.name)
		}
	}
	return nil
}
