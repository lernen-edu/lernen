// Package reflection implements Stage 5 of the forge: an open mentor
// dialogue that ends with the user articulating what they built, plus
// the atomic finalize step that publishes a runtime-shaped manifest
// to ~/.local/share/lernen/manifests/<curriculum-id>/.
//
// The package is leaf-shaped (no imports from other forge stages) for
// the same reason as scaffold/: forge.go is the only orchestrator that
// composes stages together.
package reflection

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// CurrentSchemaVersion is the version Save writes and Load expects.
// Bump only when the on-disk shape changes incompatibly.
const CurrentSchemaVersion = 1

// ReflectionResult is the persisted shape of profile/reflection.yaml
// AND the typed input to Finalize. The same value flows through
// SaveReflection → resume → Finalize.
type ReflectionResult struct {
	SchemaVersion int              `yaml:"schema_version"`
	AuthoredAt    time.Time        `yaml:"authored_at"`
	Curriculum    CurriculumNaming `yaml:"curriculum"`
	Articulation  Articulation     `yaml:"articulation"`
	LicenseNote   string           `yaml:"license_note,omitempty"`
	ForgeLog      string           `yaml:"forge_log_markdown"`
}

// CurriculumNaming carries the user-chosen curriculum-id (slug used as
// the manifest dir name) and display name.
type CurriculumNaming struct {
	ID   string `yaml:"id"`
	Name string `yaml:"name"`
}

// Articulation captures the user's words on the three reflection topics.
// If the user did not address a topic during the dialogue, the structurer
// must record "User did not articulate this in reflection." rather than
// invent content. Empty strings are rejected by Validate.
type Articulation struct {
	TierTheory      string `yaml:"tier_theory"`
	ChosenRationale string `yaml:"chosen_rationale"`
	RemainingGaps   string `yaml:"remaining_gaps"`
}

// slugRe matches the curriculum-id contract from spec §5.1: lowercase
// alphanumerics and hyphens; no leading/trailing hyphen.
var slugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*[a-z0-9]$|^[a-z0-9]$`)

// codeBlockRe matches a fenced code block whose body has four or more
// non-fence lines. Defensive PRD §4.5 firewall check (full integration
// deferred per spec §2).
var codeBlockRe = regexp.MustCompile("(?ms)^```[^\\n]*\\n(?:[^\\n]*\\n){4,}```\\s*$")

// requiredHeadings are the markdown section headings every forge_log.md
// must contain (matches the structurer prompt's contract in spec §6.2).
var requiredHeadings = []string{
	"## Goals (Stage 0)",
	"## Starting point (Stage 1)",
	"## Recommendation (Stage 2)",
	"## Ingestion (Stage 3)",
	"## Classification (Stage 4 Pass 1)",
	"## Per-chapter scaffolds (Stage 4 Pass 2)",
	"## Reflection (Stage 5)",
	"### Tier semantics, in your words",
	"### Why this curriculum, in your words",
	"### Gaps that remain",
}

// Validate enforces the schema rules. Returns the first violation found
// with a field-named error.
func (r *ReflectionResult) Validate() error {
	if r.SchemaVersion != CurrentSchemaVersion {
		return fmt.Errorf("reflection: schema_version = %d; want %d", r.SchemaVersion, CurrentSchemaVersion)
	}
	if r.AuthoredAt.IsZero() {
		return fmt.Errorf("reflection: authored_at must be set")
	}
	id := r.Curriculum.ID
	if len(id) > 64 {
		return fmt.Errorf("reflection: curriculum.id = %q is longer than 64 chars", id)
	}
	if !slugRe.MatchString(id) {
		return fmt.Errorf("reflection: curriculum.id = %q does not match slug shape ^[a-z0-9][a-z0-9-]*[a-z0-9]$", id)
	}
	if strings.TrimSpace(r.Curriculum.Name) == "" {
		return fmt.Errorf("reflection: curriculum.name must be non-empty")
	}
	if len(r.Curriculum.Name) > 120 {
		return fmt.Errorf("reflection: curriculum.name is longer than 120 chars")
	}
	if strings.TrimSpace(r.Articulation.TierTheory) == "" {
		return fmt.Errorf("reflection: articulation.tier_theory must be non-empty")
	}
	if strings.TrimSpace(r.Articulation.ChosenRationale) == "" {
		return fmt.Errorf("reflection: articulation.chosen_rationale must be non-empty")
	}
	if strings.TrimSpace(r.Articulation.RemainingGaps) == "" {
		return fmt.Errorf("reflection: articulation.remaining_gaps must be non-empty")
	}
	if strings.TrimSpace(r.ForgeLog) == "" {
		return fmt.Errorf("reflection: forge_log_markdown must be non-empty")
	}
	for _, h := range requiredHeadings {
		if !strings.Contains(r.ForgeLog, h) {
			return fmt.Errorf("reflection: forge_log_markdown missing required heading %q", h)
		}
	}
	if codeBlockRe.MatchString(r.ForgeLog) {
		return fmt.Errorf("reflection: forge_log_markdown contains a fenced code block of 4+ lines (Phase 1 firewall)")
	}
	return nil
}
