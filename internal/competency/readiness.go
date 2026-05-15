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
}

// Aggregate folds every demonstrated_clean demonstration in the
// persisted state into a per-competency tally, resolves thresholds,
// and returns one CompetencyStatus per manifest competency plus one
// per orphan (sorted: manifest competencies in manifest order, then
// orphans by id). Only outcome == demonstrated_clean counts toward
// thresholds (PRD §4.7).
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
	if state != nil {
		for _, cc := range state.CompletedChapters {
			for _, d := range cc.Demonstrations {
				if d.Outcome != progress.OutcomeDemonstratedClean {
					continue
				}
				tl := get(d.CompetencyID)
				tl.clean++
				tl.chapters[cc.ChapterID] = struct{}{}
				if d.PracticeMode {
					tl.practice++
				}
			}
		}
	}

	var out []CompetencyStatus
	inManifest := map[string]struct{}{}
	if curr != nil {
		for i := range curr.Competencies {
			c := &curr.Competencies[i]
			inManifest[c.ID] = struct{}{}
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
			}
			st.Met = st.CleanDemonstrations >= st.MinDemonstrations &&
				st.DistinctChapters >= st.MinChapters &&
				st.PracticeModeDemos >= st.MinPracticeMode
			out = append(out, st)
		}
	}

	var orphans []string
	for id := range tallies {
		if _, ok := inManifest[id]; !ok {
			orphans = append(orphans, id)
		}
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
