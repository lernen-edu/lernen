package gate

import (
	"context"

	_ "embed"

	"github.com/lernen-edu/lernen/internal/backends"
	"github.com/lernen-edu/lernen/internal/languages"
	"github.com/lernen-edu/lernen/internal/phase1"
)

//go:embed gate_debug_tutor.md
var debugTutorPrompt string

// RunDebugFixture materializes one debug workdir and grades the fix
// objectively via the TestRunner. It NEVER touches a backend — the
// inverted-assistance tutor (DebugTutorReply) is opt-in and called
// only if the learner asks for a hint.
func RunDebugFixture(ctx context.Context, dataRoot, curriculumID string, df languages.DebugFixture, runner languages.TestRunner, wait submitWaiter) (ComponentOutcome, string, error) {
	wd, err := materialize(dataRoot, curriculumID, df.ID, "Fix the program in solution.py so the tests pass.", df.TestScaffold)
	if err != nil {
		return OutcomeInfraError, "", err
	}
	if err := writeBrokenProgram(wd, df.BrokenProgram); err != nil {
		return OutcomeInfraError, "", err
	}
	<-wait(0) // debug is not wall-clock timed (spec §5.3); 0 => user-driven submit
	res, runErr := runner.Run(ctx, wd)
	return gradeToOutcome(res, runErr), wd, nil
}

// DebugTutorReply runs one Socratic turn through the reused Phase 1
// firewall (phase1.Filter) so a tutor that leaks code is stripped.
func DebugTutorReply(ctx context.Context, be backends.Backend, learnerMsg, transcript string) (string, error) {
	user := "Recent transcript:\n" + transcript + "\n\nLearner: " + learnerMsg
	resp, err := be.Chat(ctx, []backends.Message{{Role: backends.RoleUser, Content: user}}, debugTutorPrompt)
	if err != nil {
		return "", err
	}
	filtered, _ := phase1.Filter(resp.Content)
	return filtered, nil
}
