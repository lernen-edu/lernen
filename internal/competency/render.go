package competency

import (
	"fmt"
	"sort"
	"strings"

	"github.com/lernen-edu/lernen/internal/curriculum"
	"github.com/lernen-edu/lernen/internal/progress"
)

// tierOrder gives foundation-first display ordering.
func tierRank(t curriculum.Tier) int {
	switch t {
	case curriculum.TierFoundation:
		return 0
	case curriculum.TierFluency:
		return 1
	case curriculum.TierMastery:
		return 2
	default:
		return 3
	}
}

// Render produces the read-only /competency table: one row per
// competency (foundation first), demonstration / chapter / practice
// counts against resolved thresholds, a Met marker, a gate-ready
// summary line, and an orphan footnote when applicable. Pure: no AI
// call, cannot fail.
func Render(state *progress.State, curr *curriculum.Curriculum) string {
	statuses := Aggregate(state, curr)

	var manifest []CompetencyStatus
	var orphans []CompetencyStatus
	for _, s := range statuses {
		if s.InManifest {
			manifest = append(manifest, s)
		} else {
			orphans = append(orphans, s)
		}
	}
	if len(manifest) == 0 && len(orphans) == 0 {
		return "This curriculum declares no competencies."
	}

	sort.SliceStable(manifest, func(i, j int) bool {
		return tierRank(manifest[i].Tier) < tierRank(manifest[j].Tier)
	})

	var b strings.Builder
	b.WriteString("Competency progress:\n\n")
	for _, s := range manifest {
		mark := "▢"
		if s.Met {
			mark = "✓"
		}
		name := s.Name
		if name == "" {
			name = s.ID
		}
		fmt.Fprintf(&b, "  %s  %-28s [%s]  demos %d/%d  chapters %d/%d  practice %d/%d",
			mark, name, s.Tier,
			s.CleanDemonstrations, s.MinDemonstrations,
			s.DistinctChapters, s.MinChapters,
			s.PracticeModeDemos, s.MinPracticeMode)
		if s.WithHintDemonstrations > 0 || s.NeedsWorkDemonstrations > 0 {
			fmt.Fprintf(&b, "  (with-hint %d, needs-work %d)",
				s.WithHintDemonstrations, s.NeedsWorkDemonstrations)
		}
		b.WriteByte('\n')
	}

	ready := GateReady(statuses)
	var metFoundation, totalFoundation int
	for _, s := range manifest {
		if s.Tier == curriculum.TierFoundation {
			totalFoundation++
			if s.Met {
				metFoundation++
			}
		}
	}
	readyWord := "no"
	if ready {
		readyWord = "yes"
	}
	fmt.Fprintf(&b, "\nGate-ready: %s — %d of %d foundation competencies met\n",
		readyWord, metFoundation, totalFoundation)

	if len(orphans) > 0 {
		b.WriteString("\nEvidence for competencies no longer in the manifest (kept, not counted):\n")
		for _, s := range orphans {
			fmt.Fprintf(&b, "  · %s — %d clean demonstration(s)\n", s.ID, s.CleanDemonstrations)
		}
	}

	var mism []CompetencyStatus
	for _, s := range manifest {
		if len(s.TierMismatchedDemos) > 0 {
			mism = append(mism, s)
		}
	}
	if len(mism) > 0 {
		b.WriteString("\nDemonstrations whose claimed tier does not match the authored tier (not counted):\n")
		for _, s := range mism {
			for _, m := range s.TierMismatchedDemos {
				fmt.Fprintf(&b, "  · %s — claimed %s, authored %s (%d demonstration(s))\n",
					s.ID, m.ClaimedTier, s.Tier, m.Count)
			}
		}
	}
	return b.String()
}
