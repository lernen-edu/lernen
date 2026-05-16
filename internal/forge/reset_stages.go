package forge

import "strings"

// ResetStageNames is the canonical, ordered vocabulary of stage names
// accepted by `--reset-stage`, and the single source of truth for it:
// every surface that names the accepted stages renders this list rather
// than re-listing it. Order is pipeline order (Stage 0 → reflection).
//
// (dispatchByStageBasename's unknown-stage error reads it; the
// `--reset-stage` flag usage, Long help, and runResetStage's
// pre-mutation guard are wired onto it across the rest of the v0.3.2
// patch.)
//
// These are the *dispatch* names, deliberately distinct from
// profile.stageFilenames basenames ("classified_chapters",
// "manifest_competencies"): those are internal to the profile package,
// are never valid `--reset-stage` input, and must never be advertised
// as such (dogfood #2).
var ResetStageNames = []string{
	"goals",
	"starting_point",
	"recommendation",
	"ingestion",
	"scaffolding",
	"scaffolding-pass2",
	"reflection",
}

// ValidResetStage reports whether name is an accepted --reset-stage value.
func ValidResetStage(name string) bool {
	for _, s := range ResetStageNames {
		if s == name {
			return true
		}
	}
	return false
}

// ResetStageList renders ResetStageNames as the comma-separated list
// used verbatim in user-facing messages and flag help.
func ResetStageList() string {
	return strings.Join(ResetStageNames, ", ")
}
