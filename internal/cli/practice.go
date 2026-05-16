package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"

	"github.com/lernen-edu/lernen/internal/config"
	"github.com/lernen-edu/lernen/internal/curriculum"
	"github.com/lernen-edu/lernen/internal/docs"
	"github.com/lernen-edu/lernen/internal/languages"
	"github.com/lernen-edu/lernen/internal/phase1/practice"
	"github.com/lernen-edu/lernen/internal/progress"
	"github.com/lernen-edu/lernen/internal/tui"
)

// PracticeDeps is the dependency-injection surface for `lernen practice`.
// The production NewPracticeCmd uses ProductionPracticeDeps; tests pass
// mocks so no real toolchain is checked and no real terminal program is
// launched.
type PracticeDeps struct {
	// SessionRunner runs the practice TUI session. Production wires
	// productionPracticeRunner; tests typically capture opts and return nil.
	SessionRunner func(tui.PracticeOptions) error

	// DocsProvider constructs the docs provider used for /docs queries
	// inside the practice session. nil means /docs is unavailable.
	// Production wraps productionDocsProvider via productionPracticeDocsProvider.
	DocsProvider func() (docs.Provider, error)

	// SkipPreFlight disables the pytest/pytest-json-report pre-flight check.
	// Set to true in tests that need to exercise logic past the pre-flight
	// without requiring a real Python toolchain in the test environment.
	// Production always leaves this false.
	SkipPreFlight bool
}

// ProductionPracticeDeps returns the PracticeDeps wired for the shipped binary.
func ProductionPracticeDeps() PracticeDeps {
	return PracticeDeps{
		SessionRunner: productionPracticeRunner,
		DocsProvider:  productionPracticeDocsProvider,
	}
}

// productionPracticeDocsProvider constructs the docs provider for the practice
// session, reading the config from the default path (mirrors how ProductionDocsDeps
// wires productionDocsProvider in docs.go).
func productionPracticeDocsProvider() (docs.Provider, error) {
	cfgPath, err := ConfigFile()
	if err != nil {
		return nil, fmt.Errorf("practice: resolve config path: %w", err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return nil, err
	}
	return productionDocsProvider(&cfg)
}

// NewPracticeCmd builds the `lernen practice` Cobra command.
//
// Usage:
//
//	lernen practice <curriculum-id> [--manifest-dir <path>]
//
// The curriculum-id is the directory name under the manifests root.
// The default manifests root is ManifestsDir() (XDG-aware); tests can
// override via --manifest-dir.
func NewPracticeCmd(deps PracticeDeps) *cobra.Command {
	var manifestDir string
	cmd := &cobra.Command{
		Use:   "practice <curriculum-id>",
		Short: "Start an AI-off practice session for the given curriculum.",
		Long: `Start an AI-off practice session.

Selects the most under-practiced test-ready exercise from your completed
chapters, materializes a fresh workdir with solution.py and test_exercise.py,
and opens the practice TUI. Type /submit to run the tests; the harness grades
and records the outcome automatically. The AI tutor is completely absent from
practice mode — no backend, no network.

Pre-flight: requires pytest + pytest-json-report. If either is missing the
command prints an actionable install hint and exits non-zero without starting
a session.

Type /help inside the session for available commands.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPractice(cmd.Context(), args[0], manifestDir, deps)
		},
	}
	cmd.Flags().StringVar(&manifestDir, "manifest-dir", "",
		"Override manifests directory (default: XDG-resolved $DataDir/manifests)")
	// main() prints the returned error; suppress cobra's duplicate.
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	return cmd
}

// runPractice performs the full practice flow:
//  1. Resolve manifest dir and load curriculum.
//  2. Load progress (shared helper; nil == no progress yet).
//  3. SelectExercise; ErrNoPracticeReady ⇒ stdout message + exit 0.
//  4. Resolve language adapter; hard pre-flight for pytest + pytest-json-report.
//  5. Materialize workdir.
//  6. Build PracticeOptions (OnSubmit closure grades + records + saves).
//  7. Run session via injected SessionRunner.
func runPractice(ctx context.Context, curriculumID, manifestDirArg string, deps PracticeDeps) error {
	if deps.SessionRunner == nil {
		return errors.New("practice: SessionRunner is nil (programmer error)")
	}

	// Step 1: Resolve manifest dir and load curriculum.
	manifestDir, err := resolveManifestDir(manifestDirArg)
	if err != nil {
		return err
	}
	manifestPath := filepath.Join(manifestDir, curriculumID)
	curr, err := curriculum.Load(manifestPath)
	if err != nil {
		return err
	}

	// Step 2: Load progress (nil == no progress yet; SelectExercise is nil-safe).
	progressRoot, state, err := loadProgressFor(curriculumID)
	if err != nil {
		return fmt.Errorf("practice: %w", err)
	}

	// Step 3: SelectExercise; ErrNoPracticeReady ⇒ stdout + exit 0.
	sel, err := practice.SelectExercise(state, curr)
	if err != nil {
		if errors.Is(err, practice.ErrNoPracticeReady) {
			fmt.Fprintln(os.Stdout,
				"No practice-ready exercises in your completed chapters yet — finish more chapters, or re-forge so exercises carry tests.")
			return nil
		}
		return err
	}

	// Step 4: Resolve adapter; hard pre-flight for pytest + pytest-json-report.
	adapter, ok := languages.Get(curr.Metadata.Language)
	if !ok {
		return fmt.Errorf("practice: language %q has no registered adapter", curr.Metadata.Language)
	}
	if !deps.SkipPreFlight {
		tc := adapter.ToolchainCheck(ctx)
		if !toolAvailable(tc, "pytest") || !toolAvailable(tc, "pytest-json-report") {
			fmt.Fprintln(os.Stderr,
				"practice needs pytest + pytest-json-report: pip install pytest pytest-json-report")
			return fmt.Errorf("practice: pre-flight failed: pytest or pytest-json-report not available")
		}
	}

	// Step 5: Resolve absolute data root and materialize workdir.
	dataRoot, err := DataDir()
	if err != nil {
		return fmt.Errorf("practice: resolve data dir: %w", err)
	}
	workdir, err := practice.Materialize(dataRoot, curriculumID, sel.Exercise)
	if err != nil {
		return fmt.Errorf("practice: materialize workdir: %w", err)
	}

	// Step 6a: Resolve REPL.
	replCmd, replOK := adapter.REPLCommand()

	// Step 6b: Build docs handler from injected DocsProvider (best-effort; nil ⇒ unavailable).
	var onDocs func(arg string) tea.Cmd
	if deps.DocsProvider != nil {
		provider, provErr := deps.DocsProvider()
		if provErr == nil && provider != nil {
			if closer, isCloser := provider.(io.Closer); isCloser {
				// Close provider when the session ends. We defer in a goroutine
				// sense: the closure captures provider; Close is called here in
				// the "after runPractice returns" path via a dedicated defer.
				defer closer.Close()
			}
			onDocs = docsHandler(ctx, provider)
		}
		// If provErr != nil, leave onDocs nil (graceful degradation).
	}

	// Step 6c: Build OnSubmit closure — grades + records + saves.
	onSubmit := func() tea.Cmd {
		return func() tea.Msg {
			res, runErr := adapter.TestRunner().Run(ctx, workdir)
			outcome, rec := practice.Grade(res, runErr)
			if !rec {
				// Infrastructure failure: do not record; leave session open.
				errMsg := "test runner error"
				if runErr != nil {
					errMsg = fmt.Sprintf("test runner error: %v", runErr)
				}
				return tui.PracticeInfraErrorMsg{Detail: errMsg}
			}
			// Unreachable in normal flow: SelectExercise (called above before
			// this closure is built) requires at least one completed chapter,
			// so a nil state yields ErrNoPracticeReady and the session never
			// starts. Treat reaching here as a broken invariant rather than
			// silently failing to persist a graded outcome.
			if state == nil {
				panic("practice: invariant violated: OnSubmit reached with nil progress state after SelectExercise succeeded")
			}
			practice.Record(state, curr, sel, outcome, res)
			if saveErr := progress.Save(progressRoot, state); saveErr != nil {
				return tui.PracticeDoneMsg{
					Outcome: outcome,
					Summary: "WARNING: outcome not saved: " + saveErr.Error(),
				}
			}
			return tui.PracticeDoneMsg{
				Outcome: outcome,
				Summary: fmt.Sprintf("%d/%d tests", res.Passed, res.Total),
			}
		}
	}

	// Step 7: Build PracticeOptions and run session.
	opts := tui.PracticeOptions{
		Prompt:        sel.Exercise.Prompt,
		Workdir:       workdir,
		REPLAvailable: replOK,
		REPLCmd:       replCmd,
		OnSubmit:      onSubmit,
		OnDocs:        onDocs,
	}
	return deps.SessionRunner(opts)
}

// toolAvailable scans the ToolchainStatus.Tools slice for the named tool
// and returns true iff a tool with that exact Name has Available == true.
func toolAvailable(tc languages.ToolchainStatus, name string) bool {
	for _, t := range tc.Tools {
		if t.Name == name {
			return t.Available
		}
	}
	return false
}

// docsHandler builds the OnDocs closure for the practice session. The
// closure splits arg into "library [topic...]", resolves the library via
// provider.ResolveLibrary, fetches text via provider.QueryDocs, and returns
// a tea.Cmd that prints the result to scrollback. Errors degrade gracefully
// (print a note rather than crashing).
func docsHandler(ctx context.Context, provider docs.Provider) func(arg string) tea.Cmd {
	return func(arg string) tea.Cmd {
		arg = strings.TrimSpace(arg)
		if arg == "" {
			return tea.Println("Usage: /docs <library> [topic]")
		}
		parts := strings.SplitN(arg, " ", 2)
		library := parts[0]
		topic := ""
		if len(parts) == 2 {
			topic = strings.TrimSpace(parts[1])
		}

		return func() tea.Msg {
			libID, err := provider.ResolveLibrary(ctx, library)
			if err != nil {
				if errors.Is(err, docs.ErrNotConfigured) {
					return tea.Println("docs: provider not configured — set the docs API key.")()
				}
				return tea.Println(fmt.Sprintf("docs: resolve %q: %v", library, err))()
			}
			body, err := provider.QueryDocs(ctx, libID, topic, docsDefaultMaxTokens)
			if err != nil {
				return tea.Println(fmt.Sprintf("docs: query %q: %v", topic, err))()
			}
			return tea.Println(body)()
		}
	}
}

// productionPracticeRunner runs the actual Bubble Tea practice program
// against a real terminal. It mirrors productionSessionRunner exactly,
// substituting tui.NewPractice for tui.New.
//
// Inline rendering (no altscreen). See productionSessionRunner for the
// design rationale.
func productionPracticeRunner(opts tui.PracticeOptions) error {
	fmt.Print("\x1b[2J\x1b[H")
	p := tea.NewProgram(tui.NewPractice(opts))
	_, err := p.Run()
	return err
}
