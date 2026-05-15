package competency

import "github.com/lernen-edu/lernen/internal/curriculum"

// CompetencyStatus is the derived gate-readiness view for one
// competency. InManifest is false for an "orphan": a competency that
// appears in persisted demonstrations but no longer in the manifest —
// its evidence is surfaced (footnoted), never silently dropped.
type CompetencyStatus struct {
	ID   string
	Name string
	Tier curriculum.Tier

	CleanDemonstrations int
	DistinctChapters    int
	PracticeModeDemos   int

	MinDemonstrations int
	MinChapters       int
	MinPracticeMode   int

	InManifest bool
	Met        bool
}
