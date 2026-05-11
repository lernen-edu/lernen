package reflection

import (
	"strings"

	"github.com/lernen-edu/lernen/internal/curriculum"
	"github.com/lernen-edu/lernen/internal/forge/ingestion"
	"github.com/lernen-edu/lernen/internal/forge/recommendation"
	"github.com/lernen-edu/lernen/internal/forge/scaffold"
)

// ManifestCurriculum is the synthesized curriculum.yaml shape. Mirrors
// curriculum.Metadata field-for-field so a Marshal of this struct
// produces a file that curriculum.Load accepts.
//
// We keep a parallel type here rather than constructing curriculum.Metadata
// directly so the package boundary stays clean (curriculum is the
// runtime loader; reflection is the authoring side).
type ManifestCurriculum struct {
	SchemaVersion     int    `yaml:"schema_version"`
	ID                string `yaml:"id"`
	Name              string `yaml:"name"`
	AuthorAttribution string `yaml:"author_attribution"`
	Language          string `yaml:"language"`
	SourceURL         string `yaml:"source_url"`
	LicenseNote       string `yaml:"license_note"`
	Version           string `yaml:"version"`
	Phase             int    `yaml:"phase"`
	ForgeVersion      string `yaml:"forge_version"`
	AuthoredAt        string `yaml:"authored_at"`
	AuthoredBy        string `yaml:"authored_by"`
}

const defaultLicenseNote = "Authored privately via lernen forge for personal use; not redistributable as derivative work."

// transformChapter maps a forge ChapterScaffold to a runtime
// curriculum.Chapter. Drops forge-only fields (kind, explain_back_target,
// chapter-level forge_rationale, deferred). Inserts empty defaults for
// runtime-only fields the forge does not yet populate (prerequisites,
// competencies_tested, docs_libraries, exercise test_scaffold). Merges
// scaffold SourceRef.Locator into the runtime SourceRef.Note (the
// runtime SourceRef has no Locator field).
func transformChapter(s scaffold.ChapterScaffold) curriculum.Chapter {
	exercises := make([]curriculum.Exercise, 0, len(s.Exercises))
	for _, ex := range s.Exercises {
		exercises = append(exercises, curriculum.Exercise{
			ID:             ex.ID,
			Prompt:         ex.Prompt,
			Competencies:   ex.Competencies,
			TestScaffold:   "",
			ForgeRationale: ex.ForgeRationale,
		})
	}
	competenciesIntroduced := s.CompetenciesIntroduced
	if competenciesIntroduced == nil {
		competenciesIntroduced = []string{}
	}
	socr := curriculum.SocraticTemplates{}
	if s.SocraticTemplates != nil {
		socr.OnStuck = s.SocraticTemplates.OnStuck
	}
	// Merge Note + Locator. Runtime SourceRef has no Locator field, so
	// the locator (which carries "Book, Chapter N: Title") rides along
	// in Note. If Note is also non-empty, both are concatenated.
	note := strings.TrimSpace(s.SourceRef.Note)
	loc := strings.TrimSpace(s.SourceRef.Locator)
	mergedNote := loc
	if note != "" && loc != "" {
		mergedNote = note + " (locator: " + loc + ")"
	} else if note != "" {
		mergedNote = note
	}
	return curriculum.Chapter{
		ID:    s.ID,
		Title: s.Title,
		SourceRef: curriculum.SourceRef{
			Type: s.SourceRef.Type,
			URL:  s.SourceRef.URL,
			Note: mergedNote,
		},
		Prerequisites:          []string{},
		CompetenciesIntroduced: competenciesIntroduced,
		CompetenciesTested:     []string{},
		DocsLibraries:          []string{},
		Exercises:              exercises,
		SocraticTemplates:      socr,
	}
}

// transformCompetencies maps the forge's []scaffold.Competency to the
// runtime []curriculum.Competency. Structurally identical; runtime Tier
// and Layer are typed strings. When the forge's ObservableBehaviors is
// empty (an M3e Pass-2 structurer gap surfaced during dogfood — the
// prompt didn't require the field, so every competency authored before
// the prompt is fixed lacks it), fall back to a single behavior derived
// from the description so curriculum.Load's required-field check passes.
// The published competencies.yaml is editable, so the user can split or
// refine the synthesized behavior after publish.
func transformCompetencies(in []scaffold.Competency) []curriculum.Competency {
	out := make([]curriculum.Competency, 0, len(in))
	for _, c := range in {
		behaviors := c.ObservableBehaviors
		if len(behaviors) == 0 {
			if desc := strings.TrimSpace(c.Description); desc != "" {
				behaviors = []string{desc}
			} else {
				behaviors = []string{"(observable behaviors not authored; edit competencies.yaml to specify)"}
			}
		}
		out = append(out, curriculum.Competency{
			ID:                  c.ID,
			Name:                c.Name,
			Description:         c.Description,
			Tier:                curriculum.Tier(c.Tier),
			Layer:               curriculum.Layer(c.Layer),
			ObservableBehaviors: behaviors,
			ForgeRationale:      c.ForgeRationale,
		})
	}
	return out
}

// synthesizeCurriculum constructs the curriculum.yaml shape from the
// reflection result + ambient state. SourceURL is taken from
// ingestion.SourceRef only when SourceKind == "url"; for paste/pdf
// sources the URL field is left empty since SourceRef is a path or
// raw prose.
func synthesizeCurriculum(r *ReflectionResult, ing *ingestion.Ingestion, rec *recommendation.Recommendation, forgeVersion, authoredBy string) ManifestCurriculum {
	license := r.LicenseNote
	if strings.TrimSpace(license) == "" {
		license = defaultLicenseNote
	}
	sourceURL := ""
	if ing.SourceKind == "url" {
		sourceURL = ing.SourceRef
	}
	return ManifestCurriculum{
		SchemaVersion:     1,
		ID:                r.Curriculum.ID,
		Name:              r.Curriculum.Name,
		AuthorAttribution: authoredBy,
		Language:          rec.Language,
		SourceURL:         sourceURL,
		LicenseNote:       license,
		Version:           "v0.1",
		Phase:             1,
		ForgeVersion:      forgeVersion,
		AuthoredAt:        r.AuthoredAt.Format("2006-01-02"),
		AuthoredBy:        authoredBy,
	}
}
