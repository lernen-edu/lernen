package calibration

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lernen-edu/lernen/internal/backends"
	"github.com/lernen-edu/lernen/internal/forge/goals"
	"github.com/lernen-edu/lernen/internal/tui"
)

// Options configures Stage 1. Backend, SessionRunner, ProfileDir,
// Goals, SaveStartingPoint, and StartingPointPath are required. Out
// defaults to os.Stdout when nil.
//
// Goals is the Stage 0 output that Stage 1 calibrates against. The
// orchestrator (forge.Run / runStage) loads it before dispatching so
// calibration.Run always has a non-nil *Goals; see the spec
// invariant in §4.2.
//
// SaveStartingPoint and StartingPointPath are injected rather than
// imported directly so calibration remains a leaf package (profile
// imports calibration, not vice versa). The forge orchestrator wires
// these to profile.SaveStartingPoint and profile.StartingPointPath
// at the CLI boundary.
type Options struct {
	Backend           backends.Backend
	SessionRunner     func(opts tui.Options) error
	ProfileDir        string
	Goals             *goals.Goals
	SaveStartingPoint func(profileDir string, sp *StartingPoint) error
	StartingPointPath func(profileDir string) string
	ModelLabel        string
	Out               io.Writer
}

// Run executes Stage 1: opens the calibration TUI with the demanding-
// mentor system prompt (target_capability + target_project from
// goals.yaml interpolated), captures the transcript when the user
// submits /wrap, runs the structuring call, and writes
// starting_point.yaml on success. Returns an error on backend failure,
// malformed structuring output, validation failure, or if the session
// ends before /wrap.
func Run(ctx context.Context, opts Options) error {
	if opts.Backend == nil {
		return fmt.Errorf("calibration: Options.Backend is nil")
	}
	if opts.SessionRunner == nil {
		return fmt.Errorf("calibration: Options.SessionRunner is nil")
	}
	if opts.ProfileDir == "" {
		return fmt.Errorf("calibration: Options.ProfileDir is empty")
	}
	if opts.Goals == nil {
		return fmt.Errorf("calibration: Options.Goals is nil")
	}
	if opts.SaveStartingPoint == nil {
		return fmt.Errorf("calibration: Options.SaveStartingPoint is nil")
	}
	if opts.StartingPointPath == nil {
		return fmt.Errorf("calibration: Options.StartingPointPath is nil")
	}
	out := opts.Out
	if out == nil {
		out = os.Stdout
	}

	// Closure-state-after-quit pattern (see goals.Run for the
	// rationale). Bubble Tea processes Update calls serially, so the
	// goroutine writes inside a tea.Cmd, the resulting tea.Msg flows
	// back through Update before SessionRunner returns, and the read
	// in Run runs strictly after the session loop exits.
	var (
		structuringErr error
		written        *StartingPoint
	)

	wrapHandler := func(m tui.Model, _ string) (tui.Model, tea.Cmd) {
		transcript := extractTranscript(m.History())
		m, sysCmd := m.AppendSystemTurn("wrapping up — structuring your responses…")
		m = m.SetWaiting(true) // gate user input until QuitMsg fires

		cmd := func() tea.Msg {
			sp, err := Structure(ctx, opts.Backend, transcript)
			if err != nil {
				structuringErr = err
				return tea.QuitMsg{}
			}
			if err := opts.SaveStartingPoint(opts.ProfileDir, sp); err != nil {
				structuringErr = err
				return tea.QuitMsg{}
			}
			written = sp
			return tea.QuitMsg{}
		}
		return m, tea.Batch(sysCmd, cmd)
	}

	tuiOpts := tui.Options{
		Backend:         opts.Backend,
		SystemPrompt:    Stage1SystemPrompt(opts.Goals),
		HeaderText:      "Lernen Forge — Stage 1: Calibration",
		ModelLabel:      opts.ModelLabel,
		DispatchCtx:     ctx,
		IntroMessage:    "Tell the mentor what you already know — languages you've used, projects you've built, where you feel solid and where you feel shaky. /wrap when the picture is honest.",
		DisableFirewall: true,
		SlashHandlers: map[string]tui.SlashHandler{
			"wrap": wrapHandler,
		},
		SlashHandlerHelp: map[string]string{
			"wrap": "Wrap up Stage 1 and write starting_point.yaml",
		},
	}

	if err := opts.SessionRunner(tuiOpts); err != nil {
		return fmt.Errorf("calibration: session runner: %w", err)
	}
	if structuringErr != nil {
		return structuringErr
	}
	if written == nil {
		return fmt.Errorf("calibration: session ended before /wrap; starting_point.yaml not written")
	}
	fmt.Fprintf(out, "Starting point captured at %s.\n", opts.StartingPointPath(opts.ProfileDir))
	return nil
}

// extractTranscript renders user and tutor turns into a "you: ...\n
// tutor: ...\n" transcript suitable for the structuring call's user
// message. System turns are excluded — they're forge-internal status
// messages, not part of the calibration conversation. Mirrors
// goals.extractTranscript.
func extractTranscript(turns []tui.Turn) string {
	var sb strings.Builder
	for _, t := range turns {
		switch t.Role {
		case tui.RoleUser:
			sb.WriteString("you: ")
			sb.WriteString(t.Content)
			sb.WriteByte('\n')
		case tui.RoleTutor:
			sb.WriteString("tutor: ")
			sb.WriteString(t.Content)
			sb.WriteByte('\n')
		}
	}
	return strings.TrimRight(sb.String(), "\n")
}
