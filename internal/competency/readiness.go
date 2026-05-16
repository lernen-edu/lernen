package competency

import (
	"sort"

	"github.com/lernen-edu/lernen/internal/curriculum"
	"github.com/lernen-edu/lernen/internal/progress"
)

type tally struct {
	clean    int
	chapters map[string]struct{}
	practice int
	// mismatch maps a claimed tier -> count of clean demos whose
	// claimed tier did not match the authored tier (in-manifest only).
	mismatch  map[string]int
	withHint  int
	needsWork int
}

// Aggregate folds demonstrations into a per-competency tally, resolves
// thresholds, and returns one CompetencyStatus per manifest competency
// plus one per orphan (sorted: manifest competencies in manifest order,
// then orphans by id). A clean demonstration counts toward thresholds
// only if its claimed tier matches the competency's authored tier;
// mismatched clean demos are excluded and surfaced via
// TierMismatchedDemos. Only outcome == demonstrated_clean (matched)
// counts toward thresholds (PRD §4.7).
func Aggregate(state *progress.State, curr *curriculum.Curriculum) []CompetencyStatus {
	tallies := map[string]*tally{}
	get := func(id string) *tally {
		tl := tallies[id]
		if tl == nil {
			tl = &tally{chapters: map[string]struct{}{}}
			tallies[id] = tl
		}
		return tl
	}

	authored := map[string]curriculum.Tier{}
	if curr != nil {
		for i := range curr.Competencies {
			authored[curr.Competencies[i].ID] = curr.Competencies[i].Tier
		}
	}

	if state != nil {
		for _, cc := range state.CompletedChapters {
			for _, d := range cc.Demonstrations {
				switch d.Outcome {
				case progress.OutcomeDemonstratedClean:
					tl := get(d.CompetencyID)
					if at, inManifest := authored[d.CompetencyID]; inManifest && d.TierDemonstrated != string(at) {
						if tl.mismatch == nil {
							tl.mismatch = map[string]int{}
						}
						tl.mismatch[d.TierDemonstrated]++
						continue
					}
					tl.clean++
					tl.chapters[cc.ChapterID] = struct{}{}
					if d.PracticeMode {
						tl.practice++
					}
				case progress.OutcomeDemonstratedWithHint:
					get(d.CompetencyID).withHint++
				case progress.OutcomeFailed:
					get(d.CompetencyID).needsWork++
				}
				// progress.OutcomeNotAttempted and any unknown value
				// carry no signal and are intentionally ignored.
			}
		}
	}

	var out []CompetencyStatus
	if curr != nil {
		for i := range curr.Competencies {
			c := &curr.Competencies[i]
			th := Resolve(c)
			tl := tallies[c.ID]
			st := CompetencyStatus{
				ID: c.ID, Name: c.Name, Tier: c.Tier,
				MinDemonstrations: th.MinDemonstrations,
				MinChapters:       th.MinChapters,
				MinPracticeMode:   th.MinPracticeMode,
				InManifest:        true,
			}
			if tl != nil {
				st.CleanDemonstrations = tl.clean
				st.DistinctChapters = len(tl.chapters)
				st.PracticeModeDemos = tl.practice
				st.TierMismatchedDemos = sortedClaims(tl.mismatch)
				st.WithHintDemonstrations = tl.withHint
				st.NeedsWorkDemonstrations = tl.needsWork
			}
			st.Met = st.CleanDemonstrations >= st.MinDemonstrations &&
				st.DistinctChapters >= st.MinChapters &&
				st.PracticeModeDemos >= st.MinPracticeMode
			out = append(out, st)
		}
	}

	var orphans []string
	for id, tl := range tallies {
		if _, ok := authored[id]; ok {
			continue
		}
		if tl.clean == 0 {
			continue // orphan footnote surfaces clean evidence only
		}
		orphans = append(orphans, id)
	}
	sort.Strings(orphans)
	for _, id := range orphans {
		tl := tallies[id]
		out = append(out, CompetencyStatus{
			ID:                  id,
			CleanDemonstrations: tl.clean,
			DistinctChapters:    len(tl.chapters),
			PracticeModeDemos:   tl.practice,
			InManifest:          false,
			Met:                 false,
		})
	}
	return out
}

// sortedClaims converts a claimed-tier->count map into a deterministic
// slice ordered by claimed tier. Returns nil for an empty map so a
// competency with no mismatch has a nil TierMismatchedDemos.
func sortedClaims(m map[string]int) []TierMismatchedClaim {
	if len(m) == 0 {
		return nil
	}
	out := make([]TierMismatchedClaim, 0, len(m))
	for tier, n := range m {
		out = append(out, TierMismatchedClaim{ClaimedTier: tier, Count: n})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ClaimedTier < out[j].ClaimedTier })
	return out
}

// GateReady is true iff every foundation-tier, in-manifest competency
// is Met. Non-foundation tiers and orphans do not block (PRD §4.7
// gate criteria are foundation-tier; the gate exam itself is M5).
func GateReady(statuses []CompetencyStatus) bool {
	for _, s := range statuses {
		if s.InManifest && s.Tier == curriculum.TierFoundation && !s.Met {
			return false
		}
	}
	return true
}
