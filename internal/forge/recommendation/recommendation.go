package recommendation

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lernen-edu/lernen/internal/backends"
	"github.com/lernen-edu/lernen/internal/forge/calibration"
	"github.com/lernen-edu/lernen/internal/forge/goals"
	"github.com/lernen-edu/lernen/internal/tui"
)

// Options configures Stage 2. Backend, SessionRunner, ProfileDir,
// Goals, StartingPoint, Adapters, SaveRecommendation, and
// RecommendationPath are required. Out defaults to os.Stdout when nil.
//
// Goals and StartingPoint are the prior-stage outputs that Stage 2
// recommends against. The orchestrator (forge.Run / runStage) loads
// them before dispatching so recommendation.Run always has both
// non-nil; see the spec invariant in §4.2. Mirrors M3b's invariant
// pattern, extended for two preconditions.
//
// Adapters is the registered LanguageAdapter set as DTOs. The
// orchestrator builds it from languages.IDs() + languages.Get; tests
// inject custom sets to verify prompt rendering and validation
// behavior independently of the live registry.
//
// SaveRecommendation and RecommendationPath are injected rather than
// imported directly so recommendation remains a leaf package (profile
// imports recommendation, not vice versa). The forge orchestrator
// wires these to profile.SaveRecommendation and
// profile.RecommendationPath at the CLI boundary.
type Options struct {
	Backend            backends.Backend
	SessionRunner      func(opts tui.Options) error
	ProfileDir         string
	Goals              *goals.Goals
	StartingPoint      *calibration.StartingPoint
	Adapters           []AdapterInfo
	SaveRecommendation func(profileDir string, rec *Recommendation) error
	RecommendationPath func(profileDir string) string
	ModelLabel         string
	Out                io.Writer
}

// Run executes Stage 2: opens the recommendation TUI with the
// demanding-mentor system prompt (target_capability + target_project
// from goals.yaml, current_model + gaps + prior_languages from
// starting_point.yaml, and the registered adapter list interpolated),
// captures the transcript when the user submits /wrap, runs the
// structuring call, and writes recommendation.yaml on success.
// Returns an error on backend failure, malformed structuring output,
// validation failure, or if the session ends before /wrap.
func Run(ctx context.Context, opts Options) error {
	if opts.Backend == nil {
		return fmt.Errorf("recommendation: Options.Backend is nil")
	}
	if opts.SessionRunner == nil {
		return fmt.Errorf("recommendation: Options.SessionRunner is nil")
	}
	if opts.ProfileDir == "" {
		return fmt.Errorf("recommendation: Options.ProfileDir is empty")
	}
	if opts.Goals == nil {
		return fmt.Errorf("recommendation: Options.Goals is nil")
	}
	if opts.StartingPoint == nil {
		return fmt.Errorf("recommendation: Options.StartingPoint is nil")
	}
	if len(opts.Adapters) == 0 {
		return fmt.Errorf("recommendation: Options.Adapters is empty (no LanguageAdapters registered?)")
	}
	if opts.SaveRecommendation == nil {
		return fmt.Errorf("recommendation: Options.SaveRecommendation is nil")
	}
	if opts.RecommendationPath == nil {
		return fmt.Errorf("recommendation: Options.RecommendationPath is nil")
	}
	out := opts.Out
	if out == nil {
		out = os.Stdout
	}

	// Closure-state-after-quit pattern (see goals.Run / calibration.Run
	// for the rationale). Bubble Tea processes Update calls serially,
	// so the goroutine writes inside a tea.Cmd, the resulting tea.Msg
	// flows back through Update before SessionRunner returns, and the
	// read in Run runs strictly after the session loop exits.
	var (
		structuringErr error
		written        *Recommendation
	)

	adapterIDs := make([]string, len(opts.Adapters))
	for i, a := range opts.Adapters {
		adapterIDs[i] = a.ID
	}

	wrapHandler := func(m tui.Model, _ string) (tui.Model, tea.Cmd) {
		transcript := extractTranscript(m.History())
		m, sysCmd := m.AppendSystemTurn("wrapping up — structuring your responses…")
		m = m.SetWaiting(true) // gate user input until QuitMsg fires

		cmd := func() tea.Msg {
			rec, err := Structure(ctx, opts.Backend, transcript, adapterIDs)
			if err != nil {
				structuringErr = err
				return tea.QuitMsg{}
			}
			if err := opts.SaveRecommendation(opts.ProfileDir, rec); err != nil {
				structuringErr = err
				return tea.QuitMsg{}
			}
			written = rec
			return tea.QuitMsg{}
		}
		return m, tea.Batch(sysCmd, cmd)
	}

	tuiOpts := tui.Options{
		Backend:         opts.Backend,
		SystemPrompt:    Stage2SystemPrompt(opts.Goals, opts.StartingPoint, opts.Adapters),
		HeaderText:      "Lernen Forge — Stage 2: Recommendation",
		ModelLabel:      opts.ModelLabel,
		DispatchCtx:     ctx,
		IntroMessage:    "The mentor will recommend a language and curriculum based on your goals and starting point. Push back, ask questions, request alternatives. /wrap once you're aligned on the path forward.",
		DisableFirewall: true,
		SlashHandlers: map[string]tui.SlashHandler{
			"wrap": wrapHandler,
		},
		SlashHandlerHelp: map[string]string{
			"wrap": "Wrap up Stage 2 and write recommendation.yaml",
		},
	}

	if err := opts.SessionRunner(tuiOpts); err != nil {
		return fmt.Errorf("recommendation: session runner: %w", err)
	}
	if structuringErr != nil {
		return structuringErr
	}
	if written == nil {
		return fmt.Errorf("recommendation: session ended before /wrap; recommendation.yaml not written")
	}
	fmt.Fprintf(out, "Recommendation captured at %s.\n", opts.RecommendationPath(opts.ProfileDir))
	return nil
}

// extractTranscript renders user and tutor turns into a "you: ...\n
// tutor: ...\n" transcript suitable for the structuring call's user
// message. System turns are excluded — they're forge-internal status
// messages, not part of the recommendation conversation. Mirrors
// goals.extractTranscript and calibration.extractTranscript.
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
