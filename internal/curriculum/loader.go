package curriculum

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/lernen-edu/lernen/internal/languages"
)

// Load reads a curriculum manifest from dir and returns a validated
// Curriculum. The directory layout is fixed by PRD §4.4:
//
//	dir/
//	  curriculum.yaml
//	  competencies.yaml
//	  chapters/
//	    *.yaml
//
// Validation enforces the schema version, required metadata fields,
// the value vocabulary for tier/layer/phase, that the language has a
// registered adapter, and that every cross-reference (prerequisites,
// competencies introduced/tested, exercise competencies) resolves to
// an ID defined within the manifest. Unknown YAML fields are rejected
// so typos surface at load time.
func Load(dir string) (*Curriculum, error) {
	metadata, err := loadMetadata(filepath.Join(dir, "curriculum.yaml"))
	if err != nil {
		return nil, err
	}

	competencies, err := loadCompetencies(filepath.Join(dir, "competencies.yaml"))
	if err != nil {
		return nil, err
	}

	chapters, err := loadChapters(dir)
	if err != nil {
		return nil, err
	}

	c := &Curriculum{
		Dir:            dir,
		Metadata:       metadata,
		Competencies:   competencies,
		Chapters:       chapters,
		competencyByID: make(map[string]*Competency, len(competencies)),
		chapterByID:    make(map[string]*Chapter, len(chapters)),
	}

	if err := c.indexAndValidate(); err != nil {
		return nil, err
	}
	return c, nil
}

func loadMetadata(path string) (Metadata, error) {
	var m Metadata
	if err := decodeYAMLStrict(path, &m); err != nil {
		return Metadata{}, err
	}
	return m, nil
}

func loadCompetencies(path string) ([]Competency, error) {
	var comps []Competency
	if err := decodeYAMLStrict(path, &comps); err != nil {
		return nil, err
	}
	return comps, nil
}

func loadChapters(manifestDir string) ([]Chapter, error) {
	chaptersDir := filepath.Join(manifestDir, "chapters")
	entries, err := os.ReadDir(chaptersDir)
	if err != nil {
		return nil, fmt.Errorf("curriculum: read chapters dir %s: %w", chaptersDir, err)
	}

	files := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		ext := filepath.Ext(name)
		if ext != ".yaml" && ext != ".yml" {
			continue
		}
		files = append(files, name)
	}
	sort.Strings(files) // deterministic order regardless of fs

	chapters := make([]Chapter, 0, len(files))
	for _, name := range files {
		path := filepath.Join(chaptersDir, name)
		var ch Chapter
		if err := decodeYAMLStrict(path, &ch); err != nil {
			return nil, err
		}
		ch.Path = path
		chapters = append(chapters, ch)
	}
	return chapters, nil
}

// decodeYAMLStrict opens path and decodes a single YAML document into
// dest with KnownFields(true) — unknown keys are an error so typos
// surface immediately. Errors are wrapped with the file path.
func decodeYAMLStrict(path string, dest any) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("curriculum: open %s: %w", path, err)
	}
	defer f.Close()

	dec := yaml.NewDecoder(f)
	dec.KnownFields(true)

	if err := dec.Decode(dest); err != nil {
		return fmt.Errorf("curriculum: parse %s: %w", path, err)
	}

	// Reject trailing documents — a manifest file holds exactly one.
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("curriculum: %s contains multiple YAML documents; expected exactly one", path)
		}
		return fmt.Errorf("curriculum: parse trailing content in %s: %w", path, err)
	}
	return nil
}

// indexAndValidate populates the lookup maps and runs every check.
// Errors stop validation at the first failure — keeping it simple
// here is more useful than collecting every error in a v0.
func (c *Curriculum) indexAndValidate() error {
	curriculumPath := filepath.Join(c.Dir, "curriculum.yaml")

	// Schema version comes first: a wrong version means we can't trust
	// anything else we just decoded.
	if c.Metadata.SchemaVersion != CurrentSchemaVersion {
		return fmt.Errorf(
			"curriculum: %s has schema_version %d but this Lernen build supports only schema_version %d; upgrade Lernen or use a manifest authored against the supported version",
			curriculumPath, c.Metadata.SchemaVersion, CurrentSchemaVersion,
		)
	}

	// Required metadata fields.
	if c.Metadata.ID == "" {
		return fmt.Errorf("curriculum: %s is missing required field id", curriculumPath)
	}
	if c.Metadata.Name == "" {
		return fmt.Errorf("curriculum: %s is missing required field name", curriculumPath)
	}
	if c.Metadata.Language == "" {
		return fmt.Errorf("curriculum: %s is missing required field language", curriculumPath)
	}
	if c.Metadata.Version == "" {
		return fmt.Errorf("curriculum: %s is missing required field version", curriculumPath)
	}
	if c.Metadata.Phase != PhaseFluency && c.Metadata.Phase != PhaseEngineering {
		return fmt.Errorf(
			"curriculum: %s has phase %d; must be 1 (Fluency) or 2 (Engineering)",
			curriculumPath, c.Metadata.Phase,
		)
	}

	// Language must have a registered LanguageAdapter.
	if _, ok := languages.Get(c.Metadata.Language); !ok {
		return fmt.Errorf(
			"curriculum: %s declares language %q but no LanguageAdapter is registered for it; available adapters: %v",
			curriculumPath, c.Metadata.Language, languages.IDs(),
		)
	}

	// Competencies: required fields, vocabulary, uniqueness.
	for i := range c.Competencies {
		comp := &c.Competencies[i]
		if comp.ID == "" {
			return fmt.Errorf("curriculum: competencies.yaml entry %d is missing required field id", i)
		}
		if strings.TrimSpace(comp.Description) == "" {
			return fmt.Errorf("curriculum: competency %q is missing required field description", comp.ID)
		}
		if len(comp.ObservableBehaviors) == 0 {
			return fmt.Errorf("curriculum: competency %q is missing required field observable_behaviors (must list at least one behavior)", comp.ID)
		}
		switch comp.Tier {
		case TierFoundation, TierFluency, TierMastery:
		default:
			return fmt.Errorf("curriculum: competency %q has unknown tier %q; must be foundation, fluency, or mastery", comp.ID, comp.Tier)
		}
		switch comp.Layer {
		case LayerUniversal, LayerLanguageSpecific, LayerManifestSpecific:
		default:
			return fmt.Errorf("curriculum: competency %q has unknown layer %q; must be universal, language-specific, or manifest-specific", comp.ID, comp.Layer)
		}
		if _, exists := c.competencyByID[comp.ID]; exists {
			return fmt.Errorf("curriculum: duplicate competency id %q in competencies.yaml", comp.ID)
		}
		for _, tv := range []struct {
			name string
			p    *int
		}{
			{"min_demonstrations", comp.MinDemonstrations},
			{"min_chapters", comp.MinChapters},
			{"min_practice_mode", comp.MinPracticeMode},
		} {
			if tv.p != nil && *tv.p < 0 {
				return fmt.Errorf("curriculum: competency %q has negative %s (%d); must be >= 0", comp.ID, tv.name, *tv.p)
			}
		}
		c.competencyByID[comp.ID] = comp
	}

	// Chapters: uniqueness first so prerequisite lookups can resolve.
	for i := range c.Chapters {
		ch := &c.Chapters[i]
		if ch.ID == "" {
			return fmt.Errorf("curriculum: %s is missing required field id", ch.Path)
		}
		if ch.Title == "" {
			return fmt.Errorf("curriculum: %s is missing required field title", ch.Path)
		}
		if _, exists := c.chapterByID[ch.ID]; exists {
			return fmt.Errorf("curriculum: duplicate chapter id %q (%s)", ch.ID, ch.Path)
		}
		c.chapterByID[ch.ID] = ch
	}

	// Cross-references.
	for i := range c.Chapters {
		ch := &c.Chapters[i]
		for _, prereqID := range ch.Prerequisites {
			if _, ok := c.chapterByID[prereqID]; !ok {
				return fmt.Errorf("curriculum: chapter %q (%s) lists unknown prerequisite chapter %q", ch.ID, ch.Path, prereqID)
			}
		}
		for _, compID := range ch.CompetenciesIntroduced {
			if _, ok := c.competencyByID[compID]; !ok {
				return fmt.Errorf("curriculum: chapter %q (%s) introduces unknown competency %q", ch.ID, ch.Path, compID)
			}
		}
		for _, compID := range ch.CompetenciesTested {
			if _, ok := c.competencyByID[compID]; !ok {
				return fmt.Errorf("curriculum: chapter %q (%s) tests unknown competency %q", ch.ID, ch.Path, compID)
			}
		}
		for j := range ch.Exercises {
			ex := &ch.Exercises[j]
			if ex.ID == "" {
				return fmt.Errorf("curriculum: chapter %q (%s) has an exercise at index %d missing required field id", ch.ID, ch.Path, j)
			}
			for _, compID := range ex.Competencies {
				if _, ok := c.competencyByID[compID]; !ok {
					return fmt.Errorf("curriculum: exercise %q in chapter %q (%s) references unknown competency %q", ex.ID, ch.ID, ch.Path, compID)
				}
			}
		}
	}

	return nil
}
