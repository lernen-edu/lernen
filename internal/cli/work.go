package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

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
	"github.com/lernen-edu/lernen/internal/phase1/completion"
	"github.com/lernen-edu/lernen/internal/progress"
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
	var chapterFlag string
	cmd := &cobra.Command{
		Use:   "work <curriculum-id>",
		Short: "Start a Phase 1 work session for the given curriculum.",
		Long: `Start a Phase 1 work session.

Loads the named curriculum from the manifests directory, resolves the
configured backend and language adapter, runs a backend health check,
and hands control to the chat TUI. Phase 1 means the AI tutor is
firewalled — no code blocks longer than three lines reach the screen.

Chapter navigation:
  /next            advance to the next chapter; the mentor's structurer
                   summarizes what you demonstrated. /next force skips
                   the missing-competency check.
  /chapter <arg>   jump to a specific chapter: full id, 1-indexed
                   number, or the words prev / next. Does not record
                   demonstration.
  /progress        show current progress across all chapters.

Use --chapter=<id-or-number-or-prev-or-next> to open at a specific
chapter for one session (does not persist).`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runWork(cmd.Context(), workArgs{
				CurriculumID: args[0],
				ManifestDir:  manifestDir,
				Chapter:      chapterFlag,
			}, deps)
		},
	}
	cmd.Flags().StringVar(&manifestDir, "manifest-dir", "",
		"Override manifests directory (default: XDG-resolved $DataDir/manifests)")
	cmd.Flags().StringVar(&chapterFlag, "chapter", "",
		"Open at a specific chapter id, 1-indexed number, or prev/next (overrides resumed state; does not persist)")
	// main() prints the returned error; suppress cobra's duplicate.
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	return cmd
}

type workArgs struct {
	CurriculumID string
	ManifestDir  string
	Chapter      string
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

	progressRoot, err := ProgressDir()
	if err != nil {
		return fmt.Errorf("work: resolve progress dir: %w", err)
	}
	state, err := progress.Load(progressRoot, args.CurriculumID)
	if err != nil {
		return fmt.Errorf("work: load progress: %w", err)
	}
	if state == nil {
		state = progress.DefaultState(args.CurriculumID, curr.Chapters[0].ID)
	}
	chapterID := state.CurrentChapter
	if args.Chapter != "" {
		resolved, err := progress.ResolveChapter(state, curr, args.Chapter)
		if err != nil {
			return fmt.Errorf("work: --chapter: %w", err)
		}
		chapterID = resolved
	}
	// Find the chapter struct.
	var chapter *curriculum.Chapter
	for i := range curr.Chapters {
		if curr.Chapters[i].ID == chapterID {
			chapter = &curr.Chapters[i]
			break
		}
	}
	if chapter == nil {
		// Manifest may have been edited; fall back to the first chapter.
		chapter = &curr.Chapters[0]
	}

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

	// Orchestrator loop. One iteration = one TUI session = one chapter.
	// /chapter returns tea.QuitMsg via the handler closure; the loop reads
	// the closure vars, saves state, and re-enters with a fresh TUI on the
	// new chapter. /quit (without /chapter) returns with chapterChanged=false
	// and the loop exits cleanly.
	for {
		systemPrompt := phase1.RenderPhase1SystemPrompt(
			adapter.DisplayName(),
			chapter,
			curr.Competencies,
			adapter.SystemPromptAddendum(),
		)

		var (
			nextChapterID      string
			chapterChanged     bool
			nextErr            error
			curriculumComplete bool
		)

		chapterHandler := func(m tui.Model, arg string) (tui.Model, tea.Cmd) {
			resolved, err := progress.ResolveChapter(state, curr, arg)
			if err != nil {
				m, cmd := m.AppendSystemTurn(fmt.Sprintf("/chapter: %s", err.Error()))
				return m, cmd
			}
			nextChapterID = resolved
			chapterChanged = true
			return m, tea.Quit
		}

		progressHandler := func(m tui.Model, _ string) (tui.Model, tea.Cmd) {
			m, cmd := m.AppendSystemTurn(renderProgressTable(state, curr))
			return m, cmd
		}

		nextHandler := func(m tui.Model, arg string) (tui.Model, tea.Cmd) {
			force := strings.EqualFold(strings.TrimSpace(arg), "force")
			transcript := extractTranscript(m.History())
			m, sysCmd := m.AppendSystemTurn("Wrapping up the chapter — structuring the completion record…")
			m = m.SetWaiting(true)
			cmd := func() tea.Msg {
				cc, err := completion.StructureCompletion(ctx, backend, transcript, chapter, curr.Competencies)
				if err != nil {
					nextErr = fmt.Errorf("/next: structurer: %w", err)
					return tea.QuitMsg{}
				}
				if !force {
					gap := completion.MissingCompetencies(cc, chapter.CompetenciesIntroduced)
					if len(gap) > 0 {
						nextErr = fmt.Errorf("/next: mentor did not find evidence for these competencies: %s. Run /next force to advance anyway.", strings.Join(gap, ", "))
						return tea.QuitMsg{}
					}
				}
				// Harness is the source of truth for which chapter just finished;
				// overwrite the structurer's emitted chapter_id with the harness's
				// chapter pointer so the persisted record matches reality even when
				// the structurer mis-labels.
				cc.ChapterID = chapter.ID
				if cc.CompletedAt.IsZero() {
					cc.CompletedAt = time.Now().UTC()
				}
				state.CompletedChapters = append(state.CompletedChapters, *cc)
				next, err := progress.NextChapter(state, curr)
				if err != nil {
					nextErr = fmt.Errorf("/next: %w", err)
					return tea.QuitMsg{}
				}
				if next == "" {
					curriculumComplete = true
					return tea.QuitMsg{}
				}
				nextChapterID = next
				chapterChanged = true
				return tea.QuitMsg{}
			}
			return m, tea.Batch(sysCmd, cmd)
		}

		tuiOpts := tui.Options{
			Backend:      backend,
			SystemPrompt: systemPrompt,
			Curriculum:   curr,
			Chapter:      chapter,
			ModelLabel:   modelLabel(&cfg),
			DispatchCtx:  ctx,
			IntroMessage: "Ask the tutor anything about this chapter — explain a concept, walk through code, or paste an attempt for feedback. /help for commands.",
			SlashHandlers: map[string]tui.SlashHandler{
				"chapter":  chapterHandler,
				"next":     nextHandler,
				"progress": progressHandler,
			},
			SlashHandlerHelp: map[string]string{
				"chapter":  "Jump to a chapter by id, 1-indexed number, or prev/next (does not record demonstration)",
				"next":     "Advance to the next chapter (the mentor's structurer summarizes; /next force overrides missing-competency gating)",
				"progress": "Show progress against this curriculum (completed / current / pending)",
			},
		}
		if err := deps.SessionRunner(tuiOpts); err != nil {
			return err
		}

		if nextErr != nil {
			return nextErr
		}
		if curriculumComplete {
			if err := progress.Save(progressRoot, state); err != nil {
				return fmt.Errorf("work: save progress after curriculum complete: %w", err)
			}
			fmt.Fprintf(os.Stdout, "Curriculum complete — you've finished %s. Run lernen forge to author another, or lernen work %s --chapter=1 to revisit.\n", curr.Metadata.Name, curr.Metadata.ID)
			return nil
		}
		if !chapterChanged {
			return nil
		}

		state.CurrentChapter = nextChapterID
		if err := progress.Save(progressRoot, state); err != nil {
			return fmt.Errorf("work: save progress: %w", err)
		}
		for i := range curr.Chapters {
			if curr.Chapters[i].ID == nextChapterID {
				chapter = &curr.Chapters[i]
				break
			}
		}
		if chapter == nil {
			chapter = &curr.Chapters[0]
		}
	}
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

// extractTranscript renders user, tutor, and context turns into a
// "you: ...\ntutor: ...\nsystem: ...\n" transcript for the structurer's
// user message. RoleSystem turns are excluded (UI-only meta).
func extractTranscript(turns []tui.Turn) string {
	var sb strings.Builder
	for _, t := range turns {
		switch t.Role {
		case tui.RoleUser:
			sb.WriteString("you: ")
		case tui.RoleTutor:
			sb.WriteString("tutor: ")
		case tui.RoleContext:
			sb.WriteString("system: ")
		default:
			continue
		}
		sb.WriteString(t.Content)
		sb.WriteByte('\n')
	}
	return strings.TrimRight(sb.String(), "\n")
}

// renderProgressTable produces a system-turn-friendly view of the user's
// progress. Persisted-only (no LLM dispatch). Marks completed chapters
// with ✓, the current chapter with ▷, and pending chapters with ▢.
func renderProgressTable(state *progress.State, c *curriculum.Curriculum) string {
	completed := make(map[string]struct{}, len(state.CompletedChapters))
	for _, cc := range state.CompletedChapters {
		completed[cc.ChapterID] = struct{}{}
	}
	var b strings.Builder
	b.WriteString("Progress for " + c.Metadata.Name + ":\n\n")
	for i, ch := range c.Chapters {
		var mark, status string
		switch {
		case ch.ID == state.CurrentChapter:
			mark = "▷"
			status = "current"
		case mapHas(completed, ch.ID):
			mark = "✓"
			status = "completed"
		default:
			mark = "▢"
			status = "pending"
		}
		fmt.Fprintf(&b, "  %s  %d. %s — %s (%s)\n", mark, i+1, ch.Title, ch.ID, status)
	}
	b.WriteString("\nType /next to advance from the current chapter when ready, or /chapter <id|number> to jump.")
	return b.String()
}

func mapHas(m map[string]struct{}, k string) bool {
	_, ok := m[k]
	return ok
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
// Inline rendering (no altscreen). The program owns only the bottom-
// pinned region returned by Model.View(); finalized conversation turns
// emit to terminal scrollback via tea.Println. Drag-to-select and
// mouse-wheel / trackpad scroll are handled natively by the terminal
// — no program-level mouse capture, no modifier-drag workarounds, no
// mode toggle. Matches the peer-CLI convention (Claude Code, Codex,
// Gemini CLI).
//
// Clears the terminal (ANSI ESC[2J + cursor-home) before the program
// runs so the prior shell activity doesn't bleed into Lernen's first
// paint. Per-iteration of the orchestrator loop this also gives each
// chapter a clean canvas; the previous chapter's conversation stays
// in the terminal's scrollback buffer (which is unaffected by ESC[2J
// on every supported terminal).
func productionSessionRunner(opts tui.Options) error {
	fmt.Print("\x1b[2J\x1b[H")
	p := tea.NewProgram(tui.New(opts))
	_, err := p.Run()
	return err
}
