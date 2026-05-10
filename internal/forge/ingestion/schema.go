// Package ingestion defines the M3d Stage 3 output schema and runtime.
// Mirrors internal/forge/{goals,calibration,recommendation}: same
// harness shape, same prose-heavy schema philosophy. The output
// (ingestion.yaml) is the spine Stage 4 (M3e) walks chapter-by-chapter
// for per-chapter scaffolding.
package ingestion

import (
	"fmt"
	"strings"
	"time"
)

// CurrentSchemaVersion is the schema_version M3d writes and validates.
// Bumps reserved for breaking changes; additive optional fields
// populated by later stages do not bump.
const CurrentSchemaVersion = 1

// Ingestion is the structured output of forge Stage 3. It captures
// the user-curated table of contents for the curriculum chosen in
// Stage 2, plus the input source and how the TOC was extracted.
type Ingestion struct {
	SchemaVersion     int               `yaml:"schema_version"`
	AuthoredAt        time.Time         `yaml:"authored_at"`
	SourceKind        string            `yaml:"source_kind"`        // paste | url | pdf
	SourceRef         string            `yaml:"source_ref"`         // path / URL / "user pasted prose"
	ExtractionMethod  string            `yaml:"extraction_method"`  // outline | semantic | llm | paste
	Chapters          []Chapter         `yaml:"chapters"`
	ExcludedChapters  []ExcludedChapter `yaml:"excluded_chapters,omitempty"`
	ForgeVoiceSummary string            `yaml:"forge_voice_summary"`
}

// Chapter is one entry in the curated TOC. id is a slug derived from
// title at structuring time (<curriculum-slug>-ch<NN>-<title-slug>).
// source_locator is a free-prose pointer Stage 4 passes back to the
// user — no machine semantics required.
type Chapter struct {
	ID            string `yaml:"id"`
	Title         string `yaml:"title"`
	SourceLocator string `yaml:"source_locator"`
}

// ExcludedChapter records a chapter the user (with mentor reasoning)
// chose to omit from the curriculum. Present only when the proactive-
// filtering branch produced exclusions; empty for the as-is path.
type ExcludedChapter struct {
	Title         string `yaml:"title"`
	SourceLocator string `yaml:"source_locator"`
	Reason        string `yaml:"reason"`
}

// validSourceKinds and validExtractionMethods are the closed sets the
// schema admits. Adding a value requires bumping CurrentSchemaVersion.
var validSourceKinds = map[string]struct{}{
	"paste": {},
	"url":   {},
	"pdf":   {},
}

var validExtractionMethods = map[string]struct{}{
	"outline":  {},
	"semantic": {},
	"llm":      {},
	"paste":    {},
}

// Validate enforces the M3d contract:
//   - schema_version equals CurrentSchemaVersion
//   - authored_at is non-zero
//   - source_kind is one of paste|url|pdf
//   - extraction_method is one of outline|semantic|llm|paste
//   - chapters is non-empty; every entry has non-empty id, title,
//     source_locator after trimming
//   - chapter ids are unique within the file
//   - forge_voice_summary is non-empty after trimming
//   - excluded_chapters entries (if any) have non-empty title,
//     source_locator, reason
func (i *Ingestion) Validate() error {
	if i.SchemaVersion != CurrentSchemaVersion {
		return fmt.Errorf("ingestion: schema_version %d unsupported; want %d", i.SchemaVersion, CurrentSchemaVersion)
	}
	if i.AuthoredAt.IsZero() {
		return fmt.Errorf("ingestion: authored_at is zero")
	}
	if _, ok := validSourceKinds[i.SourceKind]; !ok {
		return fmt.Errorf("ingestion: source_kind %q invalid; want one of paste|url|pdf", i.SourceKind)
	}
	if _, ok := validExtractionMethods[i.ExtractionMethod]; !ok {
		return fmt.Errorf("ingestion: extraction_method %q invalid; want one of outline|semantic|llm|paste", i.ExtractionMethod)
	}
	if len(i.Chapters) == 0 {
		return fmt.Errorf("ingestion: chapters is empty; want at least one chapter")
	}
	seen := make(map[string]int, len(i.Chapters))
	for idx, c := range i.Chapters {
		if strings.TrimSpace(c.ID) == "" {
			return fmt.Errorf("ingestion: chapters[%d].id is empty", idx)
		}
		if strings.TrimSpace(c.Title) == "" {
			return fmt.Errorf("ingestion: chapters[%d].title is empty", idx)
		}
		if strings.TrimSpace(c.SourceLocator) == "" {
			return fmt.Errorf("ingestion: chapters[%d].source_locator is empty", idx)
		}
		if prior, ok := seen[c.ID]; ok {
			return fmt.Errorf("ingestion: duplicate chapter id %q at chapters[%d] (first seen at chapters[%d])", c.ID, idx, prior)
		}
		seen[c.ID] = idx
	}
	if strings.TrimSpace(i.ForgeVoiceSummary) == "" {
		return fmt.Errorf("ingestion: forge_voice_summary is empty")
	}
	for idx, e := range i.ExcludedChapters {
		if strings.TrimSpace(e.Title) == "" {
			return fmt.Errorf("ingestion: excluded_chapters[%d].title is empty", idx)
		}
		if strings.TrimSpace(e.SourceLocator) == "" {
			return fmt.Errorf("ingestion: excluded_chapters[%d].source_locator is empty", idx)
		}
		if strings.TrimSpace(e.Reason) == "" {
			return fmt.Errorf("ingestion: excluded_chapters[%d].reason is empty", idx)
		}
	}
	return nil
}
