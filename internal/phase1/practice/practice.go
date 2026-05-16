// Package practice implements AI-off practice mode: exercise
// selection, workdir materialization, objective grading via the
// language TestRunner, and recording the outcome into progress state.
// No AI backend is involved (PRD §4.6).
package practice

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/lernen-edu/lernen/internal/competency"
	"github.com/lernen-edu/lernen/internal/curriculum"
	"github.com/lernen-edu/lernen/internal/languages"
	"github.com/lernen-edu/lernen/internal/progress"
)

// ErrNoPracticeReady means no completed-chapter exercise has a runnable
// test_scaffold targeting an under-practiced foundation competency.
var ErrNoPracticeReady = errors.New("practice: no practice-ready exercises in completed chapters")

// Selection is the chosen exercise plus its source chapter id.
type Selection struct {
	Exercise  curriculum.Exercise
	ChapterID string
}

// SelectExercise picks the most under-practiced, test-ready exercise
// from a completed chapter. Deterministic: weight desc, then manifest
// chapter order, then exercise index.
func SelectExercise(state *progress.State, curr *curriculum.Curriculum) (Selection, error) {
	completed := map[string]bool{}
	if state != nil {
		for _, cc := range state.CompletedChapters {
			completed[cc.ChapterID] = true
		}
	}

	under := map[string]bool{}
	for _, s := range competency.Aggregate(state, curr) {
		if s.InManifest && s.Tier == curriculum.TierFoundation && s.PracticeModeDemos < s.MinPracticeMode {
			under[s.ID] = true
		}
	}

	type cand struct {
		sel     Selection
		weight  int
		chapIdx int
		exIdx   int
	}
	var cands []cand
	if curr != nil {
		for ci := range curr.Chapters {
			ch := &curr.Chapters[ci]
			if !completed[ch.ID] {
				continue
			}
			for ei := range ch.Exercises {
				e := ch.Exercises[ei]
				if strings.TrimSpace(e.TestScaffold) == "" {
					continue
				}
				w := 0
				for _, cid := range e.Competencies {
					if under[cid] {
						w++
					}
				}
				if w == 0 {
					continue
				}
				cands = append(cands, cand{
					sel:     Selection{Exercise: e, ChapterID: ch.ID},
					weight:  w, chapIdx: ci, exIdx: ei,
				})
			}
		}
	}
	if len(cands) == 0 {
		return Selection{}, ErrNoPracticeReady
	}
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].weight != cands[j].weight {
			return cands[i].weight > cands[j].weight
		}
		if cands[i].chapIdx != cands[j].chapIdx {
			return cands[i].chapIdx < cands[j].chapIdx
		}
		return cands[i].exIdx < cands[j].exIdx
	})
	return cands[0].sel, nil
}

// Materialize writes a fresh per-attempt workdir under
// <dataRoot>/practice/<curriculumID>/<exerciseID>-<UTC ts>/ containing
// solution.py (empty), test_exercise.py (verbatim scaffold), and
// PROMPT.md. Returns the absolute workdir.
func Materialize(dataRoot, curriculumID string, e curriculum.Exercise) (string, error) {
	ts := time.Now().UTC().Format("20060102T150405Z")
	dir := filepath.Join(dataRoot, "practice", curriculumID, e.ID+"-"+ts)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("practice: create workdir: %w", err)
	}
	write := func(name, body string) error {
		return os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644)
	}
	if err := write("solution.py", ""); err != nil {
		return "", fmt.Errorf("practice: write solution.py: %w", err)
	}
	if err := write("test_exercise.py", e.TestScaffold); err != nil {
		return "", fmt.Errorf("practice: write test_exercise.py: %w", err)
	}
	prompt := e.Prompt + "\n\n---\nEdit solution.py in your own editor, then /submit.\n"
	if err := write("PROMPT.md", prompt); err != nil {
		return "", fmt.Errorf("practice: write PROMPT.md: %w", err)
	}
	return dir, nil
}

// Grade maps a TestRunner result to a recorded outcome. runErr != nil
// is an infrastructure failure: do not record (rec=false). A ran suite
// with failures records `failed` (honest, no gate credit).
func Grade(res languages.TestResult, runErr error) (outcome string, rec bool) {
	if runErr != nil {
		return "", false
	}
	if res.Total > 0 && res.Failed == 0 {
		return progress.OutcomeDemonstratedClean, true
	}
	return progress.OutcomeFailed, true
}

// Record appends one PracticeMode demonstration per manifest-known
// competency on the exercise to the source chapter's most recent
// ChapterCompletion. Mutates state in place; caller persists via
// progress.Save. Reuses the v0.3.0 reserved PracticeMode field — no
// schema change.
func Record(state *progress.State, curr *curriculum.Curriculum, sel Selection, outcome string, res languages.TestResult) {
	idx := -1
	for i := range state.CompletedChapters {
		if state.CompletedChapters[i].ChapterID == sel.ChapterID {
			idx = i // keep scanning → last match
		}
	}
	if idx < 0 {
		return
	}
	// Build a local id→competency map by iterating the slice directly.
	// curr.Competency() uses an internal lookup map that is only populated
	// by Load; iterating curr.Competencies works for both loader-built and
	// test-constructed Curriculum values (mirrors competency.Aggregate).
	byID := map[string]*curriculum.Competency{}
	if curr != nil {
		for i := range curr.Competencies {
			byID[curr.Competencies[i].ID] = &curr.Competencies[i]
		}
	}
	for _, cid := range sel.Exercise.Competencies {
		c, ok := byID[cid]
		if !ok {
			continue
		}
		state.CompletedChapters[idx].Demonstrations = append(
			state.CompletedChapters[idx].Demonstrations,
			progress.CompetencyDemonstration{
				CompetencyID:     cid,
				TierDemonstrated: string(c.Tier),
				Evidence:         fmt.Sprintf("practice exercise %s: %d/%d tests via pytest", sel.Exercise.ID, res.Passed, res.Total),
				Outcome:          outcome,
				PracticeMode:     true,
			})
	}
}
