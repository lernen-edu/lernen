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

	// Display-only struggle signal (PRD §4.7 outcomes). Never enters
	// Met or GateReady — see TestStruggleSignal_DoesNotAffectGateReadiness.
	WithHintDemonstrations  int
	NeedsWorkDemonstrations int

	MinDemonstrations int
	MinChapters       int
	MinPracticeMode   int

	// TierMismatchedDemos lists clean demonstrations excluded from the
	// counts above because their claimed tier did not match this
	// competency's authored tier. Surfaced in a Render footnote;
	// never counted toward Met.
	TierMismatchedDemos []TierMismatchedClaim

	InManifest bool
	Met        bool
}

// TierMismatchedClaim records, for one competency, how many clean
// demonstrations claimed a tier (ClaimedTier) that did not match the
// authored tier — and were therefore not counted toward gate readiness.
type TierMismatchedClaim struct {
	ClaimedTier string
	Count       int
}
