// Package scaffold implements Stage 4 of the forge: per-chapter scaffolding.
//
// Stage 4 runs in two passes:
//
//   - Pass 1 (Classification): a mentor walks the ingestion.yaml chapter list
//     and tags each chapter "orientation" or "content". /confirm-pass-1 writes
//     classified_chapters.yaml.
//
//   - Pass 2 (Scaffolding): a mentor iterates unclassified chapters in a single
//     TUI session. Per chapter, /next dispatches a structurer call against that
//     chapter's sub-transcript and writes chapter_scaffolds/<id>.yaml plus any
//     newly invented competency definitions appended to manifest_competencies.yaml.
//
// All output lives under ~/.local/share/lernen/profile/. M3f (Reflection)
// handles publishing to ~/.local/share/lernen/manifests/<curriculum-id>/.
package scaffold

import (
	"fmt"
	"strings"
	"time"
)

// CurrentSchemaVersion is the schema version stamped onto every YAML
// file written by this package. Bumps reserved for breaking changes;
// additive optional fields populated by later substeps do not bump.
const CurrentSchemaVersion = 1

// ClassifiedChapters is the Pass 1 output: one classification per chapter
// id from ingestion.yaml. Persisted to classified_chapters.yaml in profile/.
type ClassifiedChapters struct {
	SchemaVersion     int              `yaml:"schema_version"`
	AuthoredAt        time.Time        `yaml:"authored_at"`
	Classifications   []Classification `yaml:"classifications"`
	ForgeVoiceSummary string           `yaml:"forge_voice_summary"`
}

// Classification is a single chapter's kind tag plus the mentor's one-line
// rationale. ChapterID must match an entry in ingestion.yaml.chapters[].id.
type Classification struct {
	ChapterID string `yaml:"chapter_id"`
	Kind      string `yaml:"kind"` // "orientation" | "content"
	Rationale string `yaml:"rationale"`
}

// Validate enforces the structural rules. Returns the first violation found
// with a field-named error so the offending row is identifiable.
func (c *ClassifiedChapters) Validate() error {
	if c.SchemaVersion != CurrentSchemaVersion {
		return fmt.Errorf("classified_chapters: schema_version = %d; want %d", c.SchemaVersion, CurrentSchemaVersion)
	}
	if c.AuthoredAt.IsZero() {
		return fmt.Errorf("classified_chapters: authored_at must be set")
	}
	if len(c.Classifications) == 0 {
		return fmt.Errorf("classified_chapters: classifications must be non-empty")
	}
	seen := make(map[string]struct{}, len(c.Classifications))
	for i := range c.Classifications {
		if err := c.Classifications[i].Validate(); err != nil {
			return fmt.Errorf("classified_chapters: classifications[%d]: %w", i, err)
		}
		if _, dup := seen[c.Classifications[i].ChapterID]; dup {
			return fmt.Errorf("classified_chapters: duplicate chapter_id %q at classifications[%d]", c.Classifications[i].ChapterID, i)
		}
		seen[c.Classifications[i].ChapterID] = struct{}{}
	}
	if strings.TrimSpace(c.ForgeVoiceSummary) == "" {
		return fmt.Errorf("classified_chapters: forge_voice_summary must be non-empty")
	}
	return nil
}

// Validate enforces the per-row rules.
func (c *Classification) Validate() error {
	if strings.TrimSpace(c.ChapterID) == "" {
		return fmt.Errorf("chapter_id must be non-empty")
	}
	switch c.Kind {
	case "orientation", "content":
		// ok
	case "":
		return fmt.Errorf("kind must be set")
	default:
		return fmt.Errorf("kind must be one of [orientation, content]; got %q", c.Kind)
	}
	if strings.TrimSpace(c.Rationale) == "" {
		return fmt.Errorf("rationale must be non-empty")
	}
	return nil
}

// ChapterScaffold is the per-chapter file shape: id + title + source_ref +
// kind, plus kind-dependent payload. Persisted to chapter_scaffolds/<id>.yaml.
//
// For kind=orientation: ExplainBackTarget is required; competency/exercise
// fields are absent.
//
// For kind=content (Deferred=false): CompetenciesIntroduced, Exercises, and
// SocraticTemplates.OnStuck are required (skeleton floor — M3e.x adds
// on_first_engage, on_done, exercise test_scaffold, etc.).
//
// For Deferred=true (set by /skip-chapter): only id/title/kind/source_ref
// are required; the rest may be empty. The runtime tutor and Stage 5
// reflection treat deferred chapters as placeholders to revisit.
type ChapterScaffold struct {
	SchemaVersion          int                `yaml:"schema_version"`
	ID                     string             `yaml:"id"`
	Title                  string             `yaml:"title"`
	Kind                   string             `yaml:"kind"` // "orientation" | "content"
	SourceRef              SourceRef          `yaml:"source_ref"`
	ExplainBackTarget      string             `yaml:"explain_back_target,omitempty"`
	CompetenciesIntroduced []string           `yaml:"competencies_introduced,omitempty"`
	Exercises              []Exercise         `yaml:"exercises,omitempty"`
	SocraticTemplates      *SocraticTemplates `yaml:"socratic_templates,omitempty"`
	ForgeRationale         string             `yaml:"forge_rationale,omitempty"`
	Deferred               bool               `yaml:"deferred,omitempty"`
}

// SourceRef points to where the chapter lives in the source curriculum.
// Type is one of "book_chapter" | "url" | "paste". URL is optional. Locator
// is free prose, copied from ingestion.yaml's source_locator field.
type SourceRef struct {
	Type    string `yaml:"type"`
	URL     string `yaml:"url,omitempty"`
	Locator string `yaml:"locator"`
	Note    string `yaml:"note,omitempty"`
}

// Exercise is one exercise stub. Fields are prompt, competencies,
// forge_rationale, and (optionally) test_scaffold. test_scaffold is
// populated by M4c; conceptual exercises may legitimately leave it empty.
type Exercise struct {
	ID             string   `yaml:"id"`
	Prompt         string   `yaml:"prompt"`
	Competencies   []string `yaml:"competencies"`
	ForgeRationale string   `yaml:"forge_rationale"`
	// TestScaffold is a runnable pytest module (M4c). Empty for
	// conceptual exercises (legitimately not practiceable). When
	// present it must obey the solution/test naming contract
	// (imports the `solution` module; defines test_* functions).
	TestScaffold string `yaml:"test_scaffold,omitempty"`
}

// SocraticTemplates holds the per-chapter Socratic prompts. Skeleton floor
// is on_stuck only; M3e.x adds on_first_engage and on_done.
type SocraticTemplates struct {
	OnStuck []string `yaml:"on_stuck,omitempty"`
}

// Validate enforces the kind-aware structural rules. Returns the first
// violation found with a field-named error.
func (s *ChapterScaffold) Validate() error {
	if s.SchemaVersion != CurrentSchemaVersion {
		return fmt.Errorf("chapter_scaffold: schema_version = %d; want %d", s.SchemaVersion, CurrentSchemaVersion)
	}
	if strings.TrimSpace(s.ID) == "" {
		return fmt.Errorf("chapter_scaffold: id must be non-empty")
	}
	if strings.TrimSpace(s.Title) == "" {
		return fmt.Errorf("chapter_scaffold: title must be non-empty")
	}
	switch s.Kind {
	case "orientation", "content":
		// ok
	default:
		return fmt.Errorf("chapter_scaffold: kind must be one of [orientation, content]; got %q", s.Kind)
	}
	if err := s.SourceRef.Validate(); err != nil {
		return fmt.Errorf("chapter_scaffold: %w", err)
	}
	if s.Deferred {
		// Deferred stubs skip all kind-dependent payload checks.
		return nil
	}
	switch s.Kind {
	case "orientation":
		if strings.TrimSpace(s.ExplainBackTarget) == "" {
			return fmt.Errorf("chapter_scaffold: orientation requires explain_back_target")
		}
	case "content":
		if len(s.CompetenciesIntroduced) == 0 {
			return fmt.Errorf("chapter_scaffold: content requires competencies_introduced")
		}
		if len(s.Exercises) == 0 {
			return fmt.Errorf("chapter_scaffold: content requires at least one entry in exercises")
		}
		for i := range s.Exercises {
			if err := s.Exercises[i].Validate(); err != nil {
				return fmt.Errorf("chapter_scaffold: exercises[%d]: %w", i, err)
			}
		}
		if s.SocraticTemplates == nil || len(s.SocraticTemplates.OnStuck) == 0 {
			return fmt.Errorf("chapter_scaffold: content requires socratic_templates.on_stuck")
		}
	}
	return nil
}

// Validate enforces SourceRef rules.
func (r *SourceRef) Validate() error {
	switch r.Type {
	case "book_chapter", "url", "paste":
		// ok
	case "":
		return fmt.Errorf("source_ref.type must be set")
	default:
		return fmt.Errorf("source_ref.type must be one of [book_chapter, url, paste]; got %q", r.Type)
	}
	if strings.TrimSpace(r.Locator) == "" {
		return fmt.Errorf("source_ref.locator must be non-empty")
	}
	return nil
}

// Validate enforces Exercise rules.
func (e *Exercise) Validate() error {
	if strings.TrimSpace(e.ID) == "" {
		return fmt.Errorf("id must be non-empty")
	}
	if strings.TrimSpace(e.Prompt) == "" {
		return fmt.Errorf("prompt must be non-empty")
	}
	if len(e.Competencies) == 0 {
		return fmt.Errorf("competencies must be non-empty")
	}
	if strings.TrimSpace(e.ForgeRationale) == "" {
		return fmt.Errorf("forge_rationale must be non-empty")
	}
	if e.TestScaffold != "" && strings.TrimSpace(e.TestScaffold) == "" {
		return fmt.Errorf("test_scaffold must be non-whitespace when present")
	}
	return nil
}

// ManifestCompetencies is the aggregate manifest-specific competency
// definitions file. Pass 2's /next handler appends newly-invented
// competencies via the profile.AppendCompetencies helper, which
// load-modify-saves atomically and skips duplicates by ID.
type ManifestCompetencies struct {
	SchemaVersion int          `yaml:"schema_version"`
	AuthoredAt    time.Time    `yaml:"authored_at"`
	Competencies  []Competency `yaml:"competencies"`
}

// Competency is one manifest-specific competency definition. M3e produces
// only Layer="manifest-specific" entries; M3e.x will teach the mentor to
// also reference universal and language-specific competencies once those
// taxonomies ship.
type Competency struct {
	ID                  string   `yaml:"id"`
	Name                string   `yaml:"name"`
	Description         string   `yaml:"description"`
	Tier                string   `yaml:"tier"`  // "foundation" | "fluency" | "mastery"
	Layer               string   `yaml:"layer"` // always "manifest-specific" in M3e
	ObservableBehaviors []string `yaml:"observable_behaviors,omitempty"`
	ForgeRationale      string   `yaml:"forge_rationale"`
}

// Validate enforces ManifestCompetencies rules. Empty Competencies slice
// is valid (M3e starts the file empty and appends as Pass 2 progresses).
func (m *ManifestCompetencies) Validate() error {
	if m.SchemaVersion != CurrentSchemaVersion {
		return fmt.Errorf("manifest_competencies: schema_version = %d; want %d", m.SchemaVersion, CurrentSchemaVersion)
	}
	if m.AuthoredAt.IsZero() {
		return fmt.Errorf("manifest_competencies: authored_at must be set")
	}
	seen := make(map[string]struct{}, len(m.Competencies))
	for i := range m.Competencies {
		if err := m.Competencies[i].Validate(); err != nil {
			return fmt.Errorf("manifest_competencies: competencies[%d]: %w", i, err)
		}
		id := m.Competencies[i].ID
		if _, dup := seen[id]; dup {
			return fmt.Errorf("manifest_competencies: duplicate id %q at competencies[%d]", id, i)
		}
		seen[id] = struct{}{}
	}
	return nil
}

// Validate enforces Competency rules.
func (c *Competency) Validate() error {
	if strings.TrimSpace(c.ID) == "" {
		return fmt.Errorf("id must be non-empty")
	}
	if strings.TrimSpace(c.Name) == "" {
		return fmt.Errorf("name must be non-empty")
	}
	if strings.TrimSpace(c.Description) == "" {
		return fmt.Errorf("description must be non-empty")
	}
	switch c.Tier {
	case "foundation", "fluency", "mastery":
		// ok
	case "":
		return fmt.Errorf("tier must be set")
	default:
		return fmt.Errorf("tier must be one of [foundation, fluency, mastery]; got %q", c.Tier)
	}
	if c.Layer != "manifest-specific" {
		return fmt.Errorf("layer must be \"manifest-specific\"; got %q", c.Layer)
	}
	if strings.TrimSpace(c.ForgeRationale) == "" {
		return fmt.Errorf("forge_rationale must be non-empty")
	}
	return nil
}
