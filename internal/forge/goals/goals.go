package goals

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lernen-edu/lernen/internal/backends"
	"github.com/lernen-edu/lernen/internal/tui"
)

// Options configures Stage 0. Backend, SessionRunner, ProfileDir,
// SaveGoals, and GoalsPath are required. Out defaults to os.Stdout when nil.
//
// SaveGoals and GoalsPath are injected rather than imported directly so
// goals.go remains a leaf package (profile imports goals, not vice versa).
// The forge orchestrator wires these to profile.SaveGoals and
// profile.GoalsPath at the CLI boundary.
type Options struct {
	Backend       backends.Backend
	SessionRunner func(opts tui.Options) error
	ProfileDir    string
	SaveGoals     func(profileDir string, g *Goals) error
	GoalsPath     func(profileDir string) string
	ModelLabel    string
	Out           io.Writer
}

// Run executes Stage 0: opens the goal-elicitation TUI, captures the
// transcript when the user submits /wrap, runs the structuring call,
// and writes goals.yaml on success. Returns an error on backend
// failure, malformed structuring output, validation failure, or if
// the session ends before /wrap.
func Run(ctx context.Context, opts Options) error {
	if opts.Backend == nil {
		return fmt.Errorf("goals: Options.Backend is nil")
	}
	if opts.SessionRunner == nil {
		return fmt.Errorf("goals: Options.SessionRunner is nil")
	}
	if opts.ProfileDir == "" {
		return fmt.Errorf("goals: Options.ProfileDir is empty")
	}
	if opts.SaveGoals == nil {
		return fmt.Errorf("goals: Options.SaveGoals is nil")
	}
	if opts.GoalsPath == nil {
		return fmt.Errorf("goals: Options.GoalsPath is nil")
	}
	out := opts.Out
	if out == nil {
		out = os.Stdout
	}

	// State threaded through the /wrap handler closure and read after
	// the session runner returns. Safe because Bubble Tea processes
	// Update calls serially: the closure writes inside a tea.Cmd
	// goroutine; its result tea.Msg flows back through Update (and
	// thence to the cmd that triggers tea.Quit) before SessionRunner
	// returns. The closure read in Run runs strictly after the session
	// loop exits.
	var (
		structuringErr error
		written        *Goals
	)

	wrapHandler := func(m tui.Model, _ string) (tui.Model, tea.Cmd) {
		transcript := extractTranscript(m.History())
		m = m.AppendSystemTurn("wrapping up — structuring your responses…")
		m = m.SetWaiting(true) // gate user input until QuitMsg fires

		cmd := func() tea.Msg {
			g, err := Structure(ctx, opts.Backend, transcript)
			if err != nil {
				structuringErr = err
				return tea.QuitMsg{}
			}
			if err := opts.SaveGoals(opts.ProfileDir, g); err != nil {
				structuringErr = err
				return tea.QuitMsg{}
			}
			written = g
			return tea.QuitMsg{}
		}
		return m, cmd
	}

	tuiOpts := tui.Options{
		Backend:         opts.Backend,
		SystemPrompt:    Stage0SystemPrompt(),
		HeaderText:      "Lernen Forge — Stage 0: Goal Elicitation",
		ModelLabel:      opts.ModelLabel,
		DispatchCtx:     ctx,
		IntroMessage:    "Tell the mentor what you want to learn — a project to ship, a capability to gain, a domain to explore. /wrap when your goals feel clear.",
		DisableFirewall: true,
		SlashHandlers: map[string]tui.SlashHandler{
			"wrap": wrapHandler,
		},
		SlashHandlerHelp: map[string]string{
			"wrap": "Wrap up Stage 0 and write goals.yaml",
		},
	}

	if err := opts.SessionRunner(tuiOpts); err != nil {
		return fmt.Errorf("goals: session runner: %w", err)
	}
	if structuringErr != nil {
		return structuringErr
	}
	if written == nil {
		return fmt.Errorf("goals: session ended before /wrap; goals.yaml not written")
	}
	fmt.Fprintf(out, "Goals captured at %s.\n", opts.GoalsPath(opts.ProfileDir))
	return nil
}

// extractTranscript renders user and tutor turns into a "you: ...\n
// tutor: ...\n" transcript suitable for the structuring call's user
// message. System turns are excluded — they're forge-internal status
// messages, not part of the elicitation conversation.
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
