package ingestion

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
	"github.com/lernen-edu/lernen/internal/forge/recommendation"
	"github.com/lernen-edu/lernen/internal/tui"
)

// Options configures Stage 3. Backend, SessionRunner, ProfileDir,
// Goals, StartingPoint, Recommendation, SaveIngestion, and
// IngestionPath are required. Out defaults to os.Stdout when nil.
//
// Goals, StartingPoint, and Recommendation are the prior-stage outputs
// that Stage 3 structures against. The orchestrator (forge.Run) loads
// them before dispatching so ingestion.Run always receives all three
// non-nil.
//
// SaveIngestion and IngestionPath are injected rather than imported
// directly so ingestion remains a leaf package (profile imports
// ingestion, not vice versa). The forge orchestrator wires these to
// profile.SaveIngestion and profile.IngestionPath at the CLI boundary.
type Options struct {
	Backend        backends.Backend
	SessionRunner  func(opts tui.Options) error
	ProfileDir     string
	Goals          *goals.Goals
	StartingPoint  *calibration.StartingPoint
	Recommendation *recommendation.Recommendation
	SaveIngestion  func(profileDir string, ing *Ingestion) error
	IngestionPath  func(profileDir string) string
	ModelLabel     string
	Out            io.Writer
}

// Run executes Stage 3: opens the ingestion TUI with four slash
// handlers (/paste, /url, /pdf, /wrap), captures the transcript when
// the user submits /wrap, runs the structuring call, and writes
// ingestion.yaml on success. Returns an error on backend failure,
// malformed structuring output, validation failure, or if the session
// ends before /wrap.
//
// Mechanism for /url and /pdf result delivery: these handlers return a
// tea.Cmd that runs FetchURL/ReadPDF off the main goroutine and
// resolves to tui.SystemMsg{Text: "..."} — the Update loop already
// handles SystemMsg by appending a system turn and refreshing the
// viewport. No MsgHandler extension to tui.Options was needed.
func Run(ctx context.Context, opts Options) error {
	if opts.Backend == nil {
		return fmt.Errorf("ingestion: Options.Backend is nil")
	}
	if opts.SessionRunner == nil {
		return fmt.Errorf("ingestion: Options.SessionRunner is nil")
	}
	if opts.ProfileDir == "" {
		return fmt.Errorf("ingestion: Options.ProfileDir is empty")
	}
	if opts.Goals == nil {
		return fmt.Errorf("ingestion: Options.Goals is nil")
	}
	if opts.StartingPoint == nil {
		return fmt.Errorf("ingestion: Options.StartingPoint is nil")
	}
	if opts.Recommendation == nil {
		return fmt.Errorf("ingestion: Options.Recommendation is nil")
	}
	if opts.SaveIngestion == nil {
		return fmt.Errorf("ingestion: Options.SaveIngestion is nil")
	}
	if opts.IngestionPath == nil {
		return fmt.Errorf("ingestion: Options.IngestionPath is nil")
	}
	out := opts.Out
	if out == nil {
		out = os.Stdout
	}

	// Closure-state-after-quit pattern (see goals.Run / calibration.Run /
	// recommendation.Run for the rationale). Bubble Tea processes Update
	// calls serially, so the goroutine writes inside a tea.Cmd, the
	// resulting tea.Msg flows back through Update before SessionRunner
	// returns, and the reads in Run run strictly after the session loop exits.
	var (
		structuringErr error
		written        *Ingestion
	)

	// /paste: user pasted raw TOC text into the chat. This is a synchronous
	// handler — it appends a system turn guiding the user to paste and
	// discuss, then returns. No async work needed.
	pasteHandler := func(m tui.Model, _ string) (tui.Model, tea.Cmd) {
		m = m.AppendSystemTurn("Paste mode: share the table of contents or chapter list directly in chat. " +
			"The mentor will help you review and discuss it. When you're done, use /wrap.")
		return m, nil
	}

	// /url <URL>: fetch the URL and extract candidate chapters. Dispatches
	// FetchURL off the main goroutine; results land as tui.SystemMsg turns
	// in the Update loop. SetWaiting is intentionally skipped for /url and
	// /pdf — there is no command hook to clear it after SystemMsg arrives,
	// so we leave the input gated only by the normal idle state. The user
	// can keep chatting while extraction runs.
	urlHandler := func(m tui.Model, args string) (tui.Model, tea.Cmd) {
		url := strings.TrimSpace(args)
		if url == "" {
			m = m.AppendSystemTurn("Usage: /url <URL>  — provide a full https:// URL to the curriculum page.")
			return m, nil
		}
		m = m.AppendSystemTurn(fmt.Sprintf("Fetching %s — extracting chapter candidates…", url))
		cmd := func() tea.Msg {
			res, err := FetchURL(ctx, opts.Backend, url)
			if err != nil {
				return tui.SystemMsg{Text: fmt.Sprintf("URL extraction failed: %s", err)}
			}
			// ContextMsg (not SystemMsg) so the candidate list is
			// included in the conversation context the mentor sees.
			return tui.ContextMsg{Text: renderCandidates(res, url)}
		}
		return m, cmd
	}

	// /pdf <path>: read the PDF at path and extract candidate chapters.
	// Same async pattern as /url.
	pdfHandler := func(m tui.Model, args string) (tui.Model, tea.Cmd) {
		path := strings.TrimSpace(args)
		if path == "" {
			m = m.AppendSystemTurn("Usage: /pdf <path>  — provide the local filesystem path to the PDF.")
			return m, nil
		}
		m = m.AppendSystemTurn(fmt.Sprintf("Reading PDF %s — extracting chapter candidates…", path))
		cmd := func() tea.Msg {
			res, err := ReadPDF(ctx, opts.Backend, path)
			if err != nil {
				return tui.SystemMsg{Text: fmt.Sprintf("PDF extraction failed: %s", err)}
			}
			// ContextMsg (not SystemMsg) so the candidate list is
			// included in the conversation context the mentor sees.
			return tui.ContextMsg{Text: renderCandidates(res, path)}
		}
		return m, cmd
	}

	// /wrap: run the structuring call and save ingestion.yaml. Mirrors the
	// recommendation.Run wrapHandler pattern: SetWaiting(true) gates user
	// input, then the cmd runs Structure → SaveIngestion → returns tea.QuitMsg.
	wrapHandler := func(m tui.Model, _ string) (tui.Model, tea.Cmd) {
		transcript := extractTranscript(m.History())
		m = m.AppendSystemTurn("wrapping up — structuring your responses…")
		m = m.SetWaiting(true)

		cmd := func() tea.Msg {
			ing, err := Structure(ctx, opts.Backend, transcript)
			if err != nil {
				structuringErr = err
				return tea.QuitMsg{}
			}
			if err := opts.SaveIngestion(opts.ProfileDir, ing); err != nil {
				structuringErr = err
				return tea.QuitMsg{}
			}
			written = ing
			return tea.QuitMsg{}
		}
		return m, cmd
	}

	tuiOpts := tui.Options{
		Backend:         opts.Backend,
		SystemPrompt:    Stage3SystemPrompt(opts.Goals, opts.StartingPoint, opts.Recommendation),
		HeaderText:      "Lernen Forge — Stage 3: Ingestion",
		ModelLabel:      opts.ModelLabel,
		DispatchCtx:     ctx,
		IntroMessage:    "Share your curriculum source with the mentor using /paste (raw TOC text), /url <URL>, or /pdf <path>. Discuss which chapters to include and their order. /wrap once you're aligned.",
		DisableFirewall: true,
		SlashHandlers: map[string]tui.SlashHandler{
			"paste": pasteHandler,
			"url":   urlHandler,
			"pdf":   pdfHandler,
			"wrap":  wrapHandler,
		},
		SlashHandlerHelp: map[string]string{
			"paste": "Paste raw TOC/chapter text into the chat",
			"url":   "Fetch a curriculum URL and extract chapter candidates",
			"pdf":   "Read a local PDF and extract chapter candidates",
			"wrap":  "Wrap up Stage 3 and write ingestion.yaml",
		},
	}

	if err := opts.SessionRunner(tuiOpts); err != nil {
		return fmt.Errorf("ingestion: session runner: %w", err)
	}
	if structuringErr != nil {
		return structuringErr
	}
	if written == nil {
		return fmt.Errorf("ingestion: session ended before /wrap; ingestion.yaml not written")
	}
	fmt.Fprintf(out, "Ingestion captured at %s.\n", opts.IngestionPath(opts.ProfileDir))
	return nil
}

// renderCandidates formats an ExtractionResult as the system-turn
// text shown to the mentor and user after /url or /pdf extraction.
// The chapter is the primary unit on each line; Part is a tag in
// parentheses (when present); Subsections list what's inside the
// chapter in brackets — e.g.:
//
//   3. Chapter 3: Composition  (Part II)  [Section 3.1, Section 3.2]
func renderCandidates(res *ExtractionResult, source string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Extracted %d candidate chapters via %s method from %s:\n",
		len(res.Candidates), res.Method, source)
	for i, c := range res.Candidates {
		fmt.Fprintf(&sb, "  %d. %s", i+1, c.Title)
		if c.SourceLocator != c.Title && c.SourceLocator != "" {
			fmt.Fprintf(&sb, "  <%s>", c.SourceLocator)
		}
		if c.Part != "" {
			fmt.Fprintf(&sb, "  (%s)", c.Part)
		}
		if len(c.Subsections) > 0 {
			fmt.Fprintf(&sb, "  [%s]", strings.Join(c.Subsections, ", "))
		}
		sb.WriteByte('\n')
	}
	sb.WriteString("\nDiscuss with the mentor which chapters to include or exclude. /wrap when ready.")
	return sb.String()
}

// extractTranscript renders user, tutor, and context turns into a
// "you: ...\n tutor: ...\n system: ...\n" transcript for the
// structurer's user message. RoleContext turns ARE included for
// Stage 3 (unlike M3a/b/c which only had user/tutor) because they
// carry the candidate-list extraction the structurer needs to see
// alongside any user corrections that came after. RoleSystem turns
// (UI-only meta — intro, /help, /quit) remain excluded.
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
