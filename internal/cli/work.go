package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"

	"github.com/lernen-edu/lernen/internal/backends"
	"github.com/lernen-edu/lernen/internal/backends/codex"
	"github.com/lernen-edu/lernen/internal/backends/gemini"
	"github.com/lernen-edu/lernen/internal/backends/google"
	"github.com/lernen-edu/lernen/internal/backends/openai"
	"github.com/lernen-edu/lernen/internal/backends/openrouter"
	"github.com/lernen-edu/lernen/internal/config"
	"github.com/lernen-edu/lernen/internal/curriculum"
	"github.com/lernen-edu/lernen/internal/languages"
	"github.com/lernen-edu/lernen/internal/phase1"
	"github.com/lernen-edu/lernen/internal/tui"
)

// WorkDeps is the dependency-injection surface for `lernen work`. The
// production NewWorkCmd uses ProductionWorkDeps; tests pass mocks so
// no real backend is contacted and no real terminal program is launched.
type WorkDeps struct {
	// BackendFactory constructs the backend selected by the loaded
	// config. Production wires productionBackend; tests typically
	// return *fake.FakeBackend regardless of cfg.Backend.
	BackendFactory func(*config.Config) (backends.Backend, error)

	// SessionRunner runs the actual chat session. Production wires
	// productionSessionRunner (which calls tea.NewProgram(...).Run()).
	// Tests typically capture opts and return nil so the test binary
	// doesn't try to take over the terminal.
	SessionRunner func(tui.Options) error

	// ConfigPath optionally overrides the default ConfigFile() lookup.
	// Tests usually point this at a path that does not exist so
	// config.Load returns Default() instead of reading the user's
	// real config file.
	ConfigPath string
}

// ProductionWorkDeps returns the WorkDeps wired for the shipped binary.
func ProductionWorkDeps() WorkDeps {
	return WorkDeps{
		BackendFactory: productionBackend,
		SessionRunner:  productionSessionRunner,
	}
}

// NewWorkCmd builds the `lernen work` Cobra command.
//
// Usage:
//
//	lernen work <curriculum-id> [--manifest-dir <path>]
//
// The curriculum-id is the directory name under the manifests root.
// The default manifests root is ManifestsDir() (XDG-aware); tests can
// override via --manifest-dir.
func NewWorkCmd(deps WorkDeps) *cobra.Command {
	var manifestDir string
	cmd := &cobra.Command{
		Use:   "work <curriculum-id>",
		Short: "Start a Phase 1 work session for the given curriculum.",
		Long: `Start a Phase 1 work session.

Loads the named curriculum from the manifests directory, resolves the
configured backend and language adapter, runs a backend health check,
and hands control to the chat TUI. Phase 1 means the AI tutor is
firewalled — no code blocks longer than three lines reach the screen.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runWork(cmd.Context(), workArgs{
				CurriculumID: args[0],
				ManifestDir:  manifestDir,
			}, deps)
		},
	}
	cmd.Flags().StringVar(&manifestDir, "manifest-dir", "",
		"Override manifests directory (default: XDG-resolved $DataDir/manifests)")
	// main() prints the returned error; suppress cobra's duplicate.
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	return cmd
}

type workArgs struct {
	CurriculumID string
	ManifestDir  string
}

// runWork performs the resolution chain. Each step's error is wrapped
// to identify what failed so the user can act on it.
func runWork(ctx context.Context, args workArgs, deps WorkDeps) error {
	if deps.BackendFactory == nil {
		return errors.New("work: BackendFactory is nil (programmer error)")
	}
	if deps.SessionRunner == nil {
		return errors.New("work: SessionRunner is nil (programmer error)")
	}

	manifestDir, err := resolveManifestDir(args.ManifestDir)
	if err != nil {
		return err
	}
	manifestPath := filepath.Join(manifestDir, args.CurriculumID)

	curr, err := curriculum.Load(manifestPath)
	if err != nil {
		return err
	}

	adapter, ok := languages.Get(curr.Metadata.Language)
	if !ok {
		return fmt.Errorf("work: language %q has no registered adapter (the manifest loader should have caught this — internal inconsistency)", curr.Metadata.Language)
	}

	if st := adapter.ToolchainCheck(ctx); !st.OK {
		fmt.Fprintln(os.Stderr, "toolchain warnings:")
		for _, t := range st.Tools {
			if t.Available {
				continue
			}
			hint := t.Hint
			if hint == "" {
				hint = "(no install hint provided)"
			}
			fmt.Fprintf(os.Stderr, "  - %s: %s\n", t.Name, hint)
		}
		fmt.Fprintln(os.Stderr, "(M1 does not gate on toolchain; the session continues.)")
	}

	if len(curr.Chapters) == 0 {
		return fmt.Errorf("work: manifest %s has no chapters", manifestPath)
	}
	chapter := &curr.Chapters[0]

	cfgPath := deps.ConfigPath
	if cfgPath == "" {
		p, err := ConfigFile()
		if err != nil {
			return fmt.Errorf("work: resolve config path: %w", err)
		}
		cfgPath = p
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}

	backend, err := deps.BackendFactory(&cfg)
	if err != nil {
		return fmt.Errorf("work: construct backend: %w", err)
	}

	if err := backend.HealthCheck(ctx); err != nil {
		return fmt.Errorf("work: backend health check failed (%s): %w", backend.Name(), err)
	}

	systemPrompt := phase1.RenderPhase1SystemPrompt(adapter.DisplayName(), chapter.Title, adapter.SystemPromptAddendum())

	return deps.SessionRunner(tui.Options{
		Backend:      backend,
		SystemPrompt: systemPrompt,
		Curriculum:   curr,
		Chapter:      chapter,
		ModelLabel:   modelLabel(&cfg),
		DispatchCtx:  ctx,
		IntroMessage: "Ask the tutor anything about this chapter — explain a concept, walk through code, or paste an attempt for feedback. /help for commands.",
	})
}

// modelLabel produces the "<backend>/<model>" string surfaced in the
// TUI status bar. Returns just the backend name when the per-backend
// Model field is empty (CLI subprocess backends — codex, gemini —
// where the model is selected at the CLI itself, not by us).
func modelLabel(cfg *config.Config) string {
	var model string
	switch cfg.Backend {
	case "openrouter":
		model = cfg.OpenRouter.Model
	case "openai":
		model = cfg.OpenAI.Model
	case "google":
		model = cfg.Google.Model
	case "codex":
		model = cfg.Codex.Model
	case "gemini":
		model = cfg.Gemini.Model
	}
	if model == "" {
		return cfg.Backend
	}
	return cfg.Backend + "/" + model
}

// resolveManifestDir applies the flag value when set, otherwise falls
// back to ManifestsDir() (XDG-aware).
func resolveManifestDir(flag string) (string, error) {
	if flag != "" {
		return flag, nil
	}
	d, err := ManifestsDir()
	if err != nil {
		return "", fmt.Errorf("work: resolve manifests dir: %w", err)
	}
	return d, nil
}

// productionBackend constructs the inference backend selected by the
// loaded config. M2 wires openrouter, codex, and gemini; the codex/gemini
// implementations spawn the respective CLIs as subprocesses.
func productionBackend(cfg *config.Config) (backends.Backend, error) {
	switch cfg.Backend {
	case "openrouter":
		return openrouter.New(openrouter.Config{
			APIKeyEnv: cfg.OpenRouter.APIKeyEnv,
			Model:     cfg.OpenRouter.Model,
		}), nil
	case "openai":
		return openai.New(openai.Config{
			APIKeyEnv: cfg.OpenAI.APIKeyEnv,
			Model:     cfg.OpenAI.Model,
		}), nil
	case "google":
		return google.New(google.Config{
			APIKeyEnv: cfg.Google.APIKeyEnv,
			Model:     cfg.Google.Model,
		}), nil
	case "codex":
		var opts []codex.Option
		if cfg.Codex.Binary != "" && cfg.Codex.Binary != "codex" {
			opts = append(opts, codex.WithCommand(cfg.Codex.Binary))
		}
		return codex.New(codex.Config{
			APIKeyEnv: cfg.Codex.APIKeyEnv,
			Model:     cfg.Codex.Model,
		}, opts...), nil
	case "gemini":
		var opts []gemini.Option
		if cfg.Gemini.Binary != "" && cfg.Gemini.Binary != "gemini" {
			opts = append(opts, gemini.WithCommand(cfg.Gemini.Binary))
		}
		return gemini.New(gemini.Config{
			APIKeyEnv: cfg.Gemini.APIKeyEnv,
			Model:     cfg.Gemini.Model,
		}, opts...), nil
	case "fake":
		return nil, errors.New(`backend "fake" is reserved for tests; configure a real backend (e.g. backend = "openrouter") in your config`)
	default:
		return nil, fmt.Errorf("unknown backend %q", cfg.Backend)
	}
}

// productionSessionRunner runs the actual Bubble Tea program against
// a real terminal. Tests substitute a function that just captures opts.
//
// WithAltScreen gives the four-region pinned layout (header / viewport /
// input / status) the full terminal canvas — without it the layout shares
// space with whatever scrollback was already on screen.
//
// WithMouseCellMotion enables mouse-event capture so two-finger trackpad
// scroll (and mouse-wheel) reach bubbles/viewport for scrollback. Without
// it, terminals in altscreen convert scroll gestures into arrow-key
// events, which our arrow handler then routes to input-history navigation
// — surprising behavior we previously shipped and immediately reverted.
// The trade-off — that program-level mouse capture blocks the terminal's
// default click-drag for text selection — is solved the same way Claude
// Code, Codex CLI, and Gemini CLI handle it: hold Option (Terminal.app
// and iTerm2 default) or Shift (Linux / Windows Terminal) while dragging,
// and the terminal bypasses the program's mouse capture for that gesture.
// helpText documents this so the user discovers the convention.
func productionSessionRunner(opts tui.Options) error {
	p := tea.NewProgram(
		tui.New(opts),
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(),
	)
	_, err := p.Run()
	return err
}
