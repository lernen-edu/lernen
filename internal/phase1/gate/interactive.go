package gate

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/lernen-edu/lernen/internal/languages"
)

// interactive.go composes the SAME unexported primitives RunBuild /
// RunDebugFixture use (materialize / writeBrokenProgram / gradeToOutcome)
// but drives the submit decision through a caller-owned line channel and
// surfaces the workdir path to the user BEFORE blocking — which the
// monolithic RunBuild cannot do because it materializes internally. These
// are additive siblings; RunBuild / RunDebugFixture are unchanged and
// remain the pure/unit path. No internal/backends import: the AI-off
// invariant holds structurally (spec §2.5).
//
// This file owns NO stdin reader and spawns NO goroutine. The CLI (T10b)
// calls these primitives sequentially on the SAME os.Stdin (1 build + 3
// debug). A per-call internal scanner goroutine would race across
// components: component N's parked Scan() goroutine survives its /submit,
// then steals (and bufio-reads-ahead) component N+1's lines (~50/50 kernel
// wakeup), and a goroutine blocked in os.Stdin Scan() is NOT
// ctx-interruptible. The robust seam: T10b owns exactly ONE session-long
// reader goroutine and feeds pre-split, pre-trimmed lines down a channel;
// these primitives only consume.

// RunBuildInteractive materializes the build workdir, tells the user
// where to edit, then waits for a caller-fed /submit line (or the budget
// timer forcing submission) before grading exactly once. A cancelled
// context yields OutcomeInfraError + ctx.Err() and NEVER a manufactured
// FAIL (non-terminal/resumable, spec §2.4).
func RunBuildInteractive(ctx context.Context, dataRoot, curriculumID string, bf languages.BuildFixture, runner languages.TestRunner, lines <-chan string, out io.Writer) (ComponentOutcome, string, error) {
	wd, err := materialize(dataRoot, curriculumID, bf.ID, bf.Prompt, bf.TestScaffold)
	if err != nil {
		return OutcomeInfraError, "", err
	}
	fmt.Fprintf(out, "%s\n\n", bf.Prompt)
	fmt.Fprintf(out, "Edit solution.py in %s in another terminal, then type /submit here. Time budget: %s.\n", wd, bf.TimeBudget)

	submitted, err := awaitSubmit(ctx, lines, out, bf.TimeBudget)
	if err != nil {
		return OutcomeInfraError, wd, err
	}
	if !submitted {
		fmt.Fprintln(out, "Time's up — auto-submitting.")
	}
	res, runErr := runner.Run(ctx, wd)
	return gradeToOutcome(res, runErr), wd, nil
}

// RunDebugInteractive materializes the debug workdir, writes the broken
// program the learner must fix, surfaces the path, then waits for a
// caller-fed /submit line before grading once. Debug is NOT wall-clock
// timed (spec §5.3): budget 0 ⇒ no timer, only user /submit or ctx end
// the wait. ctx cancellation ⇒ OutcomeInfraError + err, never FAIL.
func RunDebugInteractive(ctx context.Context, dataRoot, curriculumID string, df languages.DebugFixture, runner languages.TestRunner, lines <-chan string, out io.Writer) (ComponentOutcome, string, error) {
	wd, err := materialize(dataRoot, curriculumID, df.ID, "Fix the program in solution.py so the tests pass.", df.TestScaffold)
	if err != nil {
		return OutcomeInfraError, "", err
	}
	if err := writeBrokenProgram(wd, df.BrokenProgram); err != nil {
		return OutcomeInfraError, "", err
	}
	fmt.Fprintf(out, "Fix the program in solution.py so the tests pass.\n\n")
	fmt.Fprintf(out, "Edit solution.py in %s in another terminal, then type /submit when fixed.\n", wd)

	if _, err := awaitSubmit(ctx, lines, out, 0); err != nil {
		return OutcomeInfraError, wd, err
	}
	res, runErr := runner.Run(ctx, wd)
	return gradeToOutcome(res, runErr), wd, nil
}

// awaitSubmit blocks until the user types /submit, the budget timer fires
// (only when budget > 0), or ctx is cancelled. It owns no goroutine, no
// scanner, no bufio — just a select loop over the caller-owned, already
// pre-trimmed line channel. It returns userSubmitted=true only for a
// voluntary /submit; a forced/expiry submit is (false, nil). A non-nil
// err (ctx) means the caller must NOT grade — it returns OutcomeInfraError
// + that err (abandoned ≠ FAIL, spec §2.4). The userSubmitted bool
// currently drives only message wording; it is surfaced for T10b/logging
// to distinguish voluntary vs forced submit.
func awaitSubmit(ctx context.Context, lines <-chan string, out io.Writer, budget time.Duration) (userSubmitted bool, err error) {
	// budget == 0 ⇒ leave timerC nil: a receive on a nil channel is never
	// ready, so the timer arm is permanently disabled (Go idiom). Debug
	// has no wall-clock budget; only /submit or ctx ends that wait.
	var timerC <-chan time.Time
	if budget > 0 {
		t := time.NewTimer(budget)
		defer t.Stop()
		timerC = t.C
	}

	for {
		select {
		case line, ok := <-lines:
			if !ok {
				// The caller's input source is gone. Nil out the local
				// channel so this arm becomes permanently not-ready —
				// without this a closed channel busy-spins the select
				// (and would flood the reprompt). Fall through to wait on
				// timer/ctx only. Closed/EOF is NOT a submit.
				lines = nil
				continue
			}
			// line is already trimmed by the caller.
			if line == "/submit" {
				return true, nil
			}
			fmt.Fprintln(out, "Type /submit when ready.")
		case <-timerC:
			// Only reachable when budget > 0 (nil channel otherwise).
			// Forced/expiry submit: the caller prints the notice.
			return false, nil
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}
}
