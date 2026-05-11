package reflection

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lernen-edu/lernen/internal/backends"
	"github.com/lernen-edu/lernen/internal/forge/calibration"
	"github.com/lernen-edu/lernen/internal/forge/goals"
	"github.com/lernen-edu/lernen/internal/forge/ingestion"
	"github.com/lernen-edu/lernen/internal/forge/recommendation"
	"github.com/lernen-edu/lernen/internal/forge/scaffold"
	"github.com/lernen-edu/lernen/internal/tui"
)

// Options configures Stage 5 Reflection. Backend, SessionRunner, ProfileDir,
// ManifestRoot, Goals, StartingPoint, Recommendation, Ingestion,
// ClassifiedChapters, SaveReflection, and Finalize are all required.
type Options struct {
	Backend       backends.Backend
	SessionRunner func(opts tui.Options) error
	ProfileDir    string
	ManifestRoot  string

	Goals              *goals.Goals
	StartingPoint      *calibration.StartingPoint
	Recommendation     *recommendation.Recommendation
	Ingestion          *ingestion.Ingestion
	ClassifiedChapters *scaffold.ClassifiedChapters
	Competencies       []scaffold.Competency
	Scaffolds          []scaffold.ChapterScaffold

	// SaveReflection persists reflection.yaml in profileDir.
	SaveReflection func(profileDir string, r *ReflectionResult) error
	// Finalize publishes the manifest and returns the published path.
	Finalize func(profileDir, manifestRoot string, r *ReflectionResult, forgeVersion, authoredBy string) (string, error)

	ForgeVersion string
	AuthoredBy   string
	ModelLabel   string
	Out          io.Writer
}

// RunReflection executes Stage 5: opens the mentor TUI with /done and /wrap
// slash handlers, dispatches the structurer on completion, persists
// reflection.yaml, calls Finalize, and prints the closing system message.
//
// Mechanism: closure-state-after-quit pattern (same as scaffold.RunPass1).
// The /done handler stashes result/error into closed-over variables;
// RunReflection reads them after SessionRunner returns.
func RunReflection(ctx context.Context, opts Options) error {
	if opts.Backend == nil {
		return fmt.Errorf("reflection: Options.Backend is nil")
	}
	if opts.SessionRunner == nil {
		return fmt.Errorf("reflection: Options.SessionRunner is nil")
	}
	if opts.ProfileDir == "" {
		return fmt.Errorf("reflection: Options.ProfileDir is empty")
	}
	if opts.ManifestRoot == "" {
		return fmt.Errorf("reflection: Options.ManifestRoot is empty")
	}
	if opts.Goals == nil {
		return fmt.Errorf("reflection: Options.Goals is nil")
	}
	if opts.StartingPoint == nil {
		return fmt.Errorf("reflection: Options.StartingPoint is nil")
	}
	if opts.Recommendation == nil {
		return fmt.Errorf("reflection: Options.Recommendation is nil")
	}
	if opts.Ingestion == nil {
		return fmt.Errorf("reflection: Options.Ingestion is nil")
	}
	if opts.ClassifiedChapters == nil {
		return fmt.Errorf("reflection: Options.ClassifiedChapters is nil")
	}
	if opts.SaveReflection == nil {
		return fmt.Errorf("reflection: Options.SaveReflection is nil")
	}
	if opts.Finalize == nil {
		return fmt.Errorf("reflection: Options.Finalize is nil")
	}

	out := opts.Out
	if out == nil {
		out = os.Stdout
	}

	// Derive defaults from the recommendation.
	defaultID := DeriveDefaultCurriculumID(opts.Recommendation.CurriculumName)
	defaultName := opts.Recommendation.CurriculumName

	// Render the mentor system prompt.
	system, err := ReflectionSystemPrompt(ReflectionPromptInput{
		Goals:               opts.Goals,
		StartingPoint:       opts.StartingPoint,
		Recommendation:      opts.Recommendation,
		Ingestion:           opts.Ingestion,
		ClassifiedChapters:  opts.ClassifiedChapters,
		Competencies:        opts.Competencies,
		Scaffolds:           opts.Scaffolds,
		DefaultCurriculumID: defaultID,
	})
	if err != nil {
		return fmt.Errorf("reflection: render mentor prompt: %w", err)
	}

	// Render forge state markdown for the structurer's context.
	forgeStateMD := renderForgeStateMarkdown(opts)

	// Closure-scoped result variables (closure-state-after-quit pattern).
	var (
		savedReflection *ReflectionResult
		manifestPath    string
		structuringErr  error
	)

	// /done handler — also registered as /wrap alias.
	doneHandler := func(m tui.Model, _ string) (tui.Model, tea.Cmd) {
		transcript := extractTranscript(m.History())
		m, sysCmd := m.AppendSystemTurn("wrapping up — assembling your forge log…")
		m = m.SetWaiting(true)

		cmd := func() tea.Msg {
			res, err := StructureReflection(ctx, opts.Backend, transcript, ReflectionDefaults{
				DefaultCurriculumID:   defaultID,
				DefaultCurriculumName: defaultName,
				ForgeStateMarkdown:    forgeStateMD,
			})
			if err != nil {
				structuringErr = fmt.Errorf("structure: %w", err)
				return tea.QuitMsg{}
			}

			// Stamp AuthoredAt if the model didn't set it.
			if res.AuthoredAt.IsZero() {
				res.AuthoredAt = time.Now().UTC()
			}

			if err := opts.SaveReflection(opts.ProfileDir, res); err != nil {
				structuringErr = fmt.Errorf("save: %w", err)
				return tea.QuitMsg{}
			}
			savedReflection = res

			path, err := opts.Finalize(opts.ProfileDir, opts.ManifestRoot, res, opts.ForgeVersion, opts.AuthoredBy)
			if err != nil {
				structuringErr = fmt.Errorf("finalize: %w", err)
				return tea.QuitMsg{}
			}
			manifestPath = path
			return tea.QuitMsg{}
		}
		return m, tea.Batch(sysCmd, cmd)
	}

	tuiOpts := tui.Options{
		Backend:         opts.Backend,
		SystemPrompt:    system,
		HeaderText:      "Lernen Forge — Stage 5: Reflection",
		ModelLabel:      opts.ModelLabel,
		DispatchCtx:     ctx,
		IntroMessage:    "Walk through what you built. /done when you're satisfied (/wrap is an alias). /quit abandons the session.",
		DisableFirewall: true,
		SlashHandlers: map[string]tui.SlashHandler{
			"done": doneHandler,
			"wrap": doneHandler,
		},
		SlashHandlerHelp: map[string]string{
			"done": "Wrap reflection and publish the manifest",
			"wrap": "Alias for /done",
		},
	}

	if err := opts.SessionRunner(tuiOpts); err != nil {
		return fmt.Errorf("reflection: session runner: %w", err)
	}
	if structuringErr != nil {
		return structuringErr
	}
	if savedReflection == nil {
		// User /quit before /done; nothing saved.
		fmt.Fprintln(out, "Reflection abandoned; nothing saved.")
		return nil
	}
	fmt.Fprintf(out, "Manifest published at %s.\nYou can now run `lernen work %s` to start Phase 1.\n",
		manifestPath, savedReflection.Curriculum.ID)
	return nil
}

// extractTranscript renders user, tutor, and context turns into a
// "you: ...\ntutor: ...\nsystem: ...\n" transcript for the structurer's
// user message. Mirrors scaffold.extractTranscript — duplicated here to
// keep reflection leaf-shaped (no forge-stage cross-imports).
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

// renderForgeStateMarkdown produces a terse markdown rendering of the
// loaded forge state for the structurer to compose around.
func renderForgeStateMarkdown(opts Options) string {
	var b strings.Builder

	b.WriteString("## Goals (Stage 0)\n\n")
	b.WriteString("**Target capability:** ")
	b.WriteString(opts.Goals.TargetCapability)
	b.WriteString("\n\n**Motivation:** ")
	b.WriteString(opts.Goals.Motivation)
	b.WriteString("\n\n**Prior attempts:** ")
	b.WriteString(opts.Goals.PriorAttempts)
	b.WriteString("\n\n**Success definition:** ")
	b.WriteString(opts.Goals.SuccessDefinition)
	b.WriteString("\n\n**Target project:** ")
	b.WriteString(opts.Goals.TargetProject)
	b.WriteString("\n\n**Forge voice:** ")
	b.WriteString(opts.Goals.ForgeVoiceSummary)
	b.WriteString("\n\n")

	b.WriteString("## Starting point (Stage 1)\n\n")
	b.WriteString("**Current model:** ")
	b.WriteString(opts.StartingPoint.CurrentModel)
	b.WriteString("\n\n**Gaps:** ")
	b.WriteString(opts.StartingPoint.Gaps)
	b.WriteString("\n\n**Prior languages:** ")
	b.WriteString(opts.StartingPoint.PriorLanguages)
	b.WriteString("\n\n**Forge voice:** ")
	b.WriteString(opts.StartingPoint.ForgeVoiceSummary)
	b.WriteString("\n\n")

	b.WriteString("## Recommendation (Stage 2)\n\n")
	b.WriteString("**Language:** ")
	b.WriteString(opts.Recommendation.Language)
	b.WriteString("\n\n**Curriculum name:** ")
	b.WriteString(opts.Recommendation.CurriculumName)
	b.WriteString("\n\n**Curriculum source:** ")
	b.WriteString(opts.Recommendation.CurriculumSource)
	b.WriteString("\n\n**Rationale:** ")
	b.WriteString(opts.Recommendation.Rationale)
	b.WriteString("\n\n**Alternatives considered:** ")
	b.WriteString(opts.Recommendation.AlternativesConsidered)
	b.WriteString("\n\n**Forge voice:** ")
	b.WriteString(opts.Recommendation.ForgeVoiceSummary)
	b.WriteString("\n\n")

	b.WriteString("## Ingestion (Stage 3)\n\n")
	b.WriteString("**Source kind:** ")
	b.WriteString(opts.Ingestion.SourceKind)
	b.WriteString("\n\n**Source ref:** ")
	b.WriteString(opts.Ingestion.SourceRef)
	b.WriteString("\n\n**Chapters:** ")
	b.WriteString(fmt.Sprintf("%d chapters ingested", len(opts.Ingestion.Chapters)))
	b.WriteString("\n\n**Forge voice:** ")
	b.WriteString(opts.Ingestion.ForgeVoiceSummary)
	b.WriteString("\n\n")

	b.WriteString("## Classification (Stage 4 Pass 1)\n\n")
	b.WriteString("**Forge voice:** ")
	b.WriteString(opts.ClassifiedChapters.ForgeVoiceSummary)
	b.WriteString("\n\n")
	for _, cl := range opts.ClassifiedChapters.Classifications {
		b.WriteString(fmt.Sprintf("- %s: %s\n", cl.ChapterID, cl.Kind))
	}
	b.WriteString("\n")

	b.WriteString("## Per-chapter scaffolds (Stage 4 Pass 2)\n\n")
	b.WriteString(fmt.Sprintf("%d scaffolds produced", len(opts.Scaffolds)))
	b.WriteString("\n")

	return b.String()
}
