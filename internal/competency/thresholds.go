// Package competency derives Phase 1 gate-readiness from the
// demonstrations the completion structurer already persisted in
// progress state. Pure functions only — no persistence, no AI calls.
// Leaf package: imports curriculum + progress types and nothing else
// from internal/.
package competency

import "github.com/lernen-edu/lernen/internal/curriculum"

// Default gate thresholds (PRD §4.7) applied when a competency does
// not author its own.
const (
	defaultMinDemonstrations = 3
	defaultMinChapters       = 2
	defaultMinPracticeMode   = 2
)

// Thresholds is the resolved per-competency gate requirement.
type Thresholds struct {
	MinDemonstrations int
	MinChapters       int
	MinPracticeMode   int
}

// Resolve returns the competency's authored thresholds, falling back
// to the PRD defaults for any field the manifest left unset.
func Resolve(c *curriculum.Competency) Thresholds {
	pick := func(p *int, def int) int {
		if p != nil {
			return *p
		}
		return def
	}
	return Thresholds{
		MinDemonstrations: pick(c.MinDemonstrations, defaultMinDemonstrations),
		MinChapters:       pick(c.MinChapters, defaultMinChapters),
		MinPracticeMode:   pick(c.MinPracticeMode, defaultMinPracticeMode),
	}
}
