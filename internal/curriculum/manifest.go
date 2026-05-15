// Package curriculum loads and validates Lernen curriculum manifests.
//
// A manifest is a directory of YAML files (PRD §4.4) authored by the user
// via the forge. The loader enforces the schema, the value vocabulary
// (tier, layer, phase), and the cross-references between chapters,
// competencies, and exercises so the runtime tutor can rely on every ID
// resolving cleanly.
//
// Lernen ships zero curriculum manifests — testdata/manifests/hello-print
// is a fixture for tests, not content. M3 lands the forge that authors
// real manifests.
package curriculum

import (
	"strings"

	"gopkg.in/yaml.v3"
)

// CurrentSchemaVersion is the only value of curriculum.yaml's
// schema_version field that this Lernen build accepts. PRE_BUILD_ANSWERS
// §14.6 specifies that loaders reject unknown values with an upgrade
// hint rather than guess at compatibility.
const CurrentSchemaVersion = 1

// Phase is the curriculum phase per PRD §3 and §4.4. Phase 1 is
// fluency-building with the AI firewall on; Phase 2 is AI-augmented
// engineering. M1 only runs Phase 1, but the loader accepts both
// values so a Phase 2 manifest can be parsed for inspection.
type Phase int

const (
	PhaseFluency     Phase = 1
	PhaseEngineering Phase = 2
)

// Tier is a competency's depth-of-mastery axis (PRD §4.2 / §4.4). The
// values match internal/languages.CompetencyTier. UnmarshalYAML
// normalizes to lowercase + trimmed so manifests authored as
// "Foundation" or " foundation " load identically.
type Tier string

const (
	TierFoundation Tier = "foundation"
	TierFluency    Tier = "fluency"
	TierMastery    Tier = "mastery"
)

// UnmarshalYAML implements yaml.Unmarshaler so the in-memory Tier
// value is always canonical. Validation against the known set still
// runs in the loader; this only ensures case and whitespace don't
// cause spurious mismatches.
func (t *Tier) UnmarshalYAML(node *yaml.Node) error {
	var s string
	if err := node.Decode(&s); err != nil {
		return err
	}
	*t = Tier(strings.TrimSpace(strings.ToLower(s)))
	return nil
}

// Layer marks where a competency is defined: shared by all languages,
// owned by a single LanguageAdapter, or specific to one manifest
// (PRD §4.4). The loader validates the value but does not enforce that a
// layer matches the place the competency is defined — the manifest
// author makes that call during forge. UnmarshalYAML normalizes case
// and trims whitespace for the same reason as Tier.
type Layer string

const (
	LayerUniversal        Layer = "universal"
	LayerLanguageSpecific Layer = "language-specific"
	LayerManifestSpecific Layer = "manifest-specific"
)

// UnmarshalYAML implements yaml.Unmarshaler so a Layer value written
// as "Language-Specific" or " universal " loads as the canonical form.
func (l *Layer) UnmarshalYAML(node *yaml.Node) error {
	var s string
	if err := node.Decode(&s); err != nil {
		return err
	}
	*l = Layer(strings.TrimSpace(strings.ToLower(s)))
	return nil
}

// Curriculum is the loaded result of a manifest directory. The lookup
// maps are populated by Load and let callers resolve IDs without
// re-walking the slices. Curriculum is intended to be loaded once at
// startup and treated as read-only thereafter; nothing here is
// concurrency-safe for mutation.
type Curriculum struct {
	// Dir is the directory the manifest was loaded from, kept for
	// error messages and lookups in surrounding tooling.
	Dir string

	Metadata     Metadata
	Competencies []Competency
	Chapters     []Chapter

	competencyByID map[string]*Competency
	chapterByID    map[string]*Chapter
}

// Competency returns the competency with the given ID and reports
// whether one exists in the manifest.
func (c *Curriculum) Competency(id string) (*Competency, bool) {
	comp, ok := c.competencyByID[id]
	return comp, ok
}

// Chapter returns the chapter with the given ID and reports whether
// one exists in the manifest.
func (c *Curriculum) Chapter(id string) (*Chapter, bool) {
	ch, ok := c.chapterByID[id]
	return ch, ok
}

// Metadata mirrors curriculum.yaml. SourceURL, LicenseNote,
// AuthorAttribution, ForgeVersion, AuthoredAt, and AuthoredBy are
// optional — they're informational fields the forge fills in, and the
// loader accepts an empty value.
type Metadata struct {
	SchemaVersion     int    `yaml:"schema_version"`
	ID                string `yaml:"id"`
	Name              string `yaml:"name"`
	AuthorAttribution string `yaml:"author_attribution"`
	Language          string `yaml:"language"`
	SourceURL         string `yaml:"source_url"`
	LicenseNote       string `yaml:"license_note"`
	Version           string `yaml:"version"`
	Phase             Phase  `yaml:"phase"`
	ForgeVersion      string `yaml:"forge_version"`
	AuthoredAt        string `yaml:"authored_at"`
	AuthoredBy        string `yaml:"authored_by"`
}

// Competency mirrors a single entry in competencies.yaml.
type Competency struct {
	ID                  string   `yaml:"id"`
	Name                string   `yaml:"name"`
	Description         string   `yaml:"description"`
	Tier                Tier     `yaml:"tier"`
	Layer               Layer    `yaml:"layer"`
	ObservableBehaviors []string `yaml:"observable_behaviors"`
	ForgeRationale      string   `yaml:"forge_rationale"`

	// Per-competency gate thresholds (PRD §4.7). Pointers so an
	// absent key is distinguishable from an authored 0. nil ⇒ the
	// competency package applies the defaults (3 / 2 / 2).
	MinDemonstrations *int `yaml:"min_demonstrations,omitempty"`
	MinChapters       *int `yaml:"min_chapters,omitempty"`
	MinPracticeMode   *int `yaml:"min_practice_mode,omitempty"`
}

// Chapter mirrors a chapters/<id>.yaml file. Path is set by the loader
// to the relative file path (within Curriculum.Dir) so error messages
// can pin a problem to its source.
type Chapter struct {
	Path string `yaml:"-"`

	ID                     string            `yaml:"id"`
	Title                  string            `yaml:"title"`
	SourceRef              SourceRef         `yaml:"source_ref"`
	Prerequisites          []string          `yaml:"prerequisites"`
	CompetenciesIntroduced []string          `yaml:"competencies_introduced"`
	CompetenciesTested     []string          `yaml:"competencies_tested"`
	DocsLibraries          []string          `yaml:"docs_libraries"`
	Exercises              []Exercise        `yaml:"exercises"`
	SocraticTemplates      SocraticTemplates `yaml:"socratic_templates"`
}

// SourceRef is the chapter's pointer back to the source curriculum.
// PRD §4.4 shows `book_chapter`; testdata uses `test_fixture`. The
// loader accepts any string value for Type.
type SourceRef struct {
	Type string `yaml:"type"`
	URL  string `yaml:"url"`
	Note string `yaml:"note"`
}

// Exercise is a single problem the learner works through.
type Exercise struct {
	ID             string   `yaml:"id"`
	Prompt         string   `yaml:"prompt"`
	Competencies   []string `yaml:"competencies"`
	TestScaffold   string   `yaml:"test_scaffold"`
	ForgeRationale string   `yaml:"forge_rationale"`
}

// SocraticTemplates carries chapter-level prompt fragments the runtime
// tutor uses at the three trigger points in PRD §4.4.
type SocraticTemplates struct {
	OnFirstEngage []string `yaml:"on_first_engage"`
	OnStuck       []string `yaml:"on_stuck"`
	OnDone        []string `yaml:"on_done"`
}
