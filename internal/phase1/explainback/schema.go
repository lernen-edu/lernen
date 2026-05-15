// Package explainback implements the Phase 1 explain-back gate: before
// the tutor engages on a help-seeking turn, one non-streaming Chat
// call classifies the turn and (when it is problem-seeking) judges
// whether the user explained what they tried. Mirrors
// internal/phase1/completion in shape — embedded prompt, retry-once.
package explainback

import (
	"fmt"
	"strings"
)

// Decision is the structurer's verdict on a pending user turn.
//
// Two staged decisions with deliberately opposite biases (spec §5.2):
//   - IsProblemSeeking — the classifier. The prompt biases ambiguous
//     turns TOWARD problem-seeking, closing the "phrase my bug as a
//     concept question" bypass.
//   - Sufficient — the evaluator, only meaningful when problem-seeking.
//     Conservative per PRD §4.6: false negatives acceptable, false
//     positives not.
type Decision struct {
	IsProblemSeeking bool   `yaml:"is_problem_seeking"`
	Sufficient       bool   `yaml:"sufficient"`
	Followup         string `yaml:"followup"`
}

// Gated reports whether this decision should hold the tutor and post
// the follow-up instead of dispatching.
func (d *Decision) Gated() bool {
	return d.IsProblemSeeking && !d.Sufficient
}

// Validate enforces the one cross-field rule: a gating decision must
// carry a non-empty follow-up to show the user.
func (d *Decision) Validate() error {
	if d.Gated() && strings.TrimSpace(d.Followup) == "" {
		return fmt.Errorf("explainback: gating decision has empty followup")
	}
	return nil
}
