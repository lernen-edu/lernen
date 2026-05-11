// Package progress owns the typed runtime user state for Phase 1
// chapter navigation: which chapter the user is currently on, which
// chapters they have completed (with structurer-emitted demonstration
// records), and the Save / Load / advance helpers.
//
// The package depends only on the runtime curriculum types (for chapter
// order lookups) and otherwise has no internal-package dependencies. It
// does not import internal/forge or internal/cli — internal/cli/work.go
// is the sole orchestrator that composes progress, completion, and
// curriculum together.
package progress

import (
	"fmt"
	"strings"
	"time"
)

// CurrentSchemaVersion is the version Save writes and Load expects.
// Bump only when the on-disk shape changes incompatibly.
const CurrentSchemaVersion = 1

// State is the persisted shape of progress/<curriculum-id>/state.yaml.
type State struct {
	SchemaVersion     int                 `yaml:"schema_version"`
	CurriculumID      string              `yaml:"curriculum_id"`
	UpdatedAt         time.Time           `yaml:"updated_at"`
	CurrentChapter    string              `yaml:"current_chapter"`
	CompletedChapters []ChapterCompletion `yaml:"completed_chapters"`
}

// ChapterCompletion is the structurer-emitted record for one finished
// chapter. Persisted in completion order.
type ChapterCompletion struct {
	ChapterID      string                    `yaml:"chapter_id"`
	Kind           string                    `yaml:"kind"`
	CompletedAt    time.Time                 `yaml:"completed_at"`
	MentorSummary  string                    `yaml:"mentor_summary"`
	Demonstrations []CompetencyDemonstration `yaml:"demonstrations,omitempty"`
	ExplainBack    string                    `yaml:"explain_back,omitempty"`
}

// CompetencyDemonstration is one piece of evidence the structurer
// extracted from the dialogue / exercise attempts during a content
// chapter.
type CompetencyDemonstration struct {
	CompetencyID     string `yaml:"competency_id"`
	TierDemonstrated string `yaml:"tier_demonstrated"`
	Evidence         string `yaml:"evidence"`
}

// Validate enforces the per-record rules independent of a State (used
// by the completion structurer before the record is folded into the
// State's CompletedChapters list). When allowMissingDemos is true,
// content chapters with zero demonstrations are accepted — this is the
// "self-attested advance" path: the harness has confirmed with the
// user that they want to advance despite missing evidence.
func (c *ChapterCompletion) Validate(allowMissingDemos bool) error {
	if strings.TrimSpace(c.ChapterID) == "" {
		return fmt.Errorf("progress: ChapterCompletion: chapter_id must be non-empty")
	}
	if strings.TrimSpace(c.MentorSummary) == "" {
		return fmt.Errorf("progress: ChapterCompletion: mentor_summary must be non-empty")
	}
	switch c.Kind {
	case "content":
		if !allowMissingDemos && len(c.Demonstrations) == 0 {
			return fmt.Errorf("progress: ChapterCompletion: content chapter has no demonstrations")
		}
		for i := range c.Demonstrations {
			d := &c.Demonstrations[i]
			if strings.TrimSpace(d.CompetencyID) == "" {
				return fmt.Errorf("progress: ChapterCompletion: demonstrations[%d].competency_id empty", i)
			}
			if strings.TrimSpace(d.TierDemonstrated) == "" {
				return fmt.Errorf("progress: ChapterCompletion: demonstrations[%d].tier_demonstrated empty", i)
			}
			if strings.TrimSpace(d.Evidence) == "" {
				return fmt.Errorf("progress: ChapterCompletion: demonstrations[%d].evidence empty", i)
			}
		}
	case "orientation":
		if strings.TrimSpace(c.ExplainBack) == "" {
			return fmt.Errorf("progress: ChapterCompletion: orientation chapter has empty explain_back")
		}
	default:
		return fmt.Errorf("progress: ChapterCompletion: unknown kind %q; want orientation|content", c.Kind)
	}
	return nil
}

// Validate enforces the schema rules. Returns the first violation found.
func (s *State) Validate() error {
	if s.SchemaVersion != CurrentSchemaVersion {
		return fmt.Errorf("progress: schema_version = %d; want %d", s.SchemaVersion, CurrentSchemaVersion)
	}
	if strings.TrimSpace(s.CurriculumID) == "" {
		return fmt.Errorf("progress: curriculum_id must be non-empty")
	}
	if strings.TrimSpace(s.CurrentChapter) == "" {
		return fmt.Errorf("progress: current_chapter must be non-empty")
	}
	seen := make(map[string]struct{}, len(s.CompletedChapters))
	for i := range s.CompletedChapters {
		c := &s.CompletedChapters[i]
		if strings.TrimSpace(c.ChapterID) == "" {
			return fmt.Errorf("progress: completed_chapters[%d].chapter_id must be non-empty", i)
		}
		if _, dup := seen[c.ChapterID]; dup {
			return fmt.Errorf("progress: completed_chapters[%d] duplicate chapter_id %q", i, c.ChapterID)
		}
		seen[c.ChapterID] = struct{}{}
		switch c.Kind {
		case "content":
			if len(c.Demonstrations) == 0 {
				return fmt.Errorf("progress: completed_chapters[%d] (%s) is content but has no demonstrations", i, c.ChapterID)
			}
			for j := range c.Demonstrations {
				d := &c.Demonstrations[j]
				if strings.TrimSpace(d.CompetencyID) == "" {
					return fmt.Errorf("progress: completed_chapters[%d].demonstrations[%d].competency_id must be non-empty", i, j)
				}
				if strings.TrimSpace(d.TierDemonstrated) == "" {
					return fmt.Errorf("progress: completed_chapters[%d].demonstrations[%d].tier_demonstrated must be non-empty", i, j)
				}
				if strings.TrimSpace(d.Evidence) == "" {
					return fmt.Errorf("progress: completed_chapters[%d].demonstrations[%d].evidence must be non-empty", i, j)
				}
			}
		case "orientation":
			if strings.TrimSpace(c.ExplainBack) == "" {
				return fmt.Errorf("progress: completed_chapters[%d] (%s) is orientation but has empty explain_back", i, c.ChapterID)
			}
		case "":
			return fmt.Errorf("progress: completed_chapters[%d] (%s) kind must be set", i, c.ChapterID)
		default:
			return fmt.Errorf("progress: completed_chapters[%d] has unknown kind %q; want orientation|content", i, c.Kind)
		}
		if strings.TrimSpace(c.MentorSummary) == "" {
			return fmt.Errorf("progress: completed_chapters[%d] (%s) has empty mentor_summary", i, c.ChapterID)
		}
	}
	return nil
}
