package gate

import (
	"context"
	"time"

	"github.com/lernen-edu/lernen/internal/languages"
	"github.com/lernen-edu/lernen/internal/phase1/practice"
	"github.com/lernen-edu/lernen/internal/progress"
)

type submitSignal struct {
	userSubmitted bool // false => timer expired, forced submit
}

// submitWaiter blocks until the user submits or the budget expires.
// Injected so tests need no real sleeps; production = realSubmitWaiter.
type submitWaiter func(budget time.Duration) <-chan submitSignal

// RunBuild materializes the build workdir, waits for submit/expiry,
// then grades exactly once via the language TestRunner. NO backend
// parameter exists on this path (spec §2.5 AI-off invariant).
func RunBuild(ctx context.Context, dataRoot, curriculumID string, bf languages.BuildFixture, runner languages.TestRunner, wait submitWaiter) (ComponentOutcome, string, error) {
	wd, err := materialize(dataRoot, curriculumID, bf.ID, bf.Prompt, bf.TestScaffold)
	if err != nil {
		return OutcomeInfraError, "", err
	}
	<-wait(bf.TimeBudget) // either user /submit or hard expiry
	res, runErr := runner.Run(ctx, wd)
	return gradeToOutcome(res, runErr), wd, nil
}

// gradeToOutcome reuses the M4c practice.Grade semantics and maps its
// (outcome, recorded) result onto the gate's 3-valued outcome.
func gradeToOutcome(res languages.TestResult, runErr error) ComponentOutcome {
	outcome, recorded := practice.Grade(res, runErr)
	if !recorded {
		return OutcomeInfraError
	}
	if outcome == progress.OutcomeDemonstratedClean {
		return OutcomePass
	}
	return OutcomeFail
}
