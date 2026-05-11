package scaffold

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
	"github.com/lernen-edu/lernen/internal/forge/ingestion"
	"github.com/lernen-edu/lernen/internal/forge/recommendation"
	"github.com/lernen-edu/lernen/internal/tui"
)

// Pass1Options configures Stage 4 Pass 1. Backend, SessionRunner,
// ProfileDir, Goals, StartingPoint, Recommendation, Ingestion,
// SaveClassifiedChapters, and ClassifiedChaptersPath are required.
// Out defaults to os.Stdout when nil.
//
// SaveClassifiedChapters and ClassifiedChaptersPath are injected rather
// than imported directly so scaffold remains a leaf package (profile
// imports scaffold, not vice versa). The forge orchestrator wires these
// to profile.SaveClassifiedChapters and profile.ClassifiedChaptersPath
// at the CLI boundary.
type Pass1Options struct {
	Backend        backends.Backend
	SessionRunner  func(opts tui.Options) error
	ProfileDir     string
	Goals          *goals.Goals
	StartingPoint  *calibration.StartingPoint
	Recommendation *recommendation.Recommendation
	Ingestion      *ingestion.Ingestion

	// SaveClassifiedChapters writes cc to the profile directory.
	SaveClassifiedChapters func(profileDir string, cc *ClassifiedChapters) error
	// ClassifiedChaptersPath returns the path for progress messages.
	ClassifiedChaptersPath func(profileDir string) string

	ModelLabel string
	Out        io.Writer
}

// RunPass1 executes Stage 4 Pass 1: opens the classification TUI with a
// /confirm-pass-1 slash handler, captures the transcript when the user
// submits /confirm-pass-1, dispatches StructureClassification, persists
// classified_chapters.yaml, and returns. Returns an error on backend
// failure, malformed structuring output, validation failure, or if the
// session ends before /confirm-pass-1.
//
// Mechanism: closure-state-after-quit pattern (same as ingestion.Run).
// The /confirm-pass-1 handler stashes the result/error into closed-over
// variables; RunPass1 reads them after SessionRunner returns.
func RunPass1(ctx context.Context, opts Pass1Options) error {
	if opts.Backend == nil {
		return fmt.Errorf("scaffold: Pass1Options.Backend is nil")
	}
	if opts.SessionRunner == nil {
		return fmt.Errorf("scaffold: Pass1Options.SessionRunner is nil")
	}
	if opts.ProfileDir == "" {
		return fmt.Errorf("scaffold: Pass1Options.ProfileDir is empty")
	}
	if opts.Goals == nil {
		return fmt.Errorf("scaffold: Pass1Options.Goals is nil")
	}
	if opts.StartingPoint == nil {
		return fmt.Errorf("scaffold: Pass1Options.StartingPoint is nil")
	}
	if opts.Recommendation == nil {
		return fmt.Errorf("scaffold: Pass1Options.Recommendation is nil")
	}
	if opts.Ingestion == nil {
		return fmt.Errorf("scaffold: Pass1Options.Ingestion is nil")
	}
	if opts.SaveClassifiedChapters == nil {
		return fmt.Errorf("scaffold: Pass1Options.SaveClassifiedChapters is nil")
	}
	if opts.ClassifiedChaptersPath == nil {
		return fmt.Errorf("scaffold: Pass1Options.ClassifiedChaptersPath is nil")
	}
	out := opts.Out
	if out == nil {
		out = os.Stdout
	}

	// Closure-state-after-quit pattern (see ingestion.Run for rationale).
	// Bubble Tea processes Update calls serially, so the goroutine writes
	// inside a tea.Cmd, the resulting tea.Msg flows back through Update
	// before SessionRunner returns, and the reads below run strictly after
	// the session loop exits.
	var (
		structuringErr error
		written        *ClassifiedChapters
	)

	// Extract chapter IDs from ingestion for the structuring call.
	chapterIDs := make([]string, len(opts.Ingestion.Chapters))
	for i, ch := range opts.Ingestion.Chapters {
		chapterIDs[i] = ch.ID
	}

	// /confirm-pass-1: run the structuring call and save classified_chapters.yaml.
	// Mirrors ingestion.Run's wrapHandler: SetWaiting(true) gates user input,
	// then the cmd runs StructureClassification → SaveClassifiedChapters →
	// returns tea.QuitMsg.
	confirmHandler := func(m tui.Model, _ string) (tui.Model, tea.Cmd) {
		transcript := extractTranscript(m.History())
		m, sysCmd := m.AppendSystemTurn("wrapping up Pass 1 — structuring your classifications…")
		m = m.SetWaiting(true)

		cmd := func() tea.Msg {
			cc, err := StructureClassification(ctx, opts.Backend, transcript, chapterIDs)
			if err != nil {
				structuringErr = err
				return tea.QuitMsg{}
			}
			if err := opts.SaveClassifiedChapters(opts.ProfileDir, cc); err != nil {
				structuringErr = err
				return tea.QuitMsg{}
			}
			written = cc
			return tea.QuitMsg{}
		}
		return m, tea.Batch(sysCmd, cmd)
	}

	tuiOpts := tui.Options{
		Backend:         opts.Backend,
		SystemPrompt:    Pass1SystemPrompt(opts.Goals, opts.StartingPoint, opts.Recommendation, opts.Ingestion),
		HeaderText:      "Lernen Forge — Stage 4 Pass 1: Classification",
		ModelLabel:      opts.ModelLabel,
		DispatchCtx:     ctx,
		IntroMessage:    "Walk the chapter list with the mentor. Each chapter gets tagged orientation or content. /confirm-pass-1 when you're done.",
		DisableFirewall: true,
		SlashHandlers: map[string]tui.SlashHandler{
			"confirm-pass-1": confirmHandler,
		},
		SlashHandlerHelp: map[string]string{
			"confirm-pass-1": "Wrap Pass 1 and write classified_chapters.yaml",
		},
	}

	if err := opts.SessionRunner(tuiOpts); err != nil {
		return fmt.Errorf("scaffold: session runner: %w", err)
	}
	if structuringErr != nil {
		return structuringErr
	}
	if written == nil {
		return fmt.Errorf("scaffold: session ended before /confirm-pass-1; classified_chapters.yaml not written")
	}
	fmt.Fprintf(out, "Classifications captured at %s.\n", opts.ClassifiedChaptersPath(opts.ProfileDir))
	return nil
}

// extractTranscript renders user, tutor, and context turns into a
// "you: ...\ntutor: ...\nsystem: ...\n" transcript for the structurer's
// user message. RoleContext turns ARE included (they carry candidate-list
// extraction context the structurer needs to see). RoleSystem turns
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

// Pass2Options configures Stage 4 Pass 2. Backend, SessionRunner,
// ProfileDir, ClassifiedChapters, SaveChapterScaffold, AppendCompetencies,
// and ListChapterScaffolds are required. Goals, StartingPoint, Recommendation,
// Ingestion, ChapterScaffoldsDir, ModelLabel, and Out are optional (but
// functionally required for a useful session). Out defaults to os.Stdout.
//
// SaveChapterScaffold, AppendCompetencies, ListChapterScaffolds, and
// ChapterScaffoldsDir are injected rather than imported directly so scaffold
// remains a leaf package. The forge orchestrator wires these to the
// corresponding profile helpers at the CLI boundary.
type Pass2Options struct {
	Backend       backends.Backend
	SessionRunner func(opts tui.Options) error
	ProfileDir    string

	Goals          *goals.Goals
	StartingPoint  *calibration.StartingPoint
	Recommendation *recommendation.Recommendation
	Ingestion      *ingestion.Ingestion

	ClassifiedChapters *ClassifiedChapters

	// ChapterScaffoldsDir returns the directory for chapter scaffold files.
	ChapterScaffoldsDir func(profileDir string) string
	// SaveChapterScaffold writes the chapter scaffold to the profile directory.
	SaveChapterScaffold func(profileDir string, s *ChapterScaffold) error
	// AppendCompetencies appends new competency definitions to the manifest.
	AppendCompetencies func(profileDir string, defs []Competency) error
	// ListChapterScaffolds returns the set of already-scaffolded chapter IDs.
	ListChapterScaffolds func(profileDir string) (map[string]bool, error)

	ModelLabel string
	Out        io.Writer
}

// RunPass2 executes Stage 4 Pass 2: opens a single TUI session and iterates
// through unscaffolded chapters in order. Per-chapter, /next dispatches
// StructureChapter and writes the scaffold + any new competencies; /skip-chapter
// writes a deferred stub; /wrap exits the session early.
//
// Resume detection: ListChapterScaffolds returns the set of already-scaffolded
// chapter IDs; RunPass2 starts at the first classified chapter not in that set.
// If all chapters are already scaffolded, RunPass2 returns nil without opening
// a TUI session.
//
// Mechanism: closure-state-after-quit pattern (same as RunPass1 and
// ingestion.Run). The slash handlers stash results/errors into closed-over
// variables; RunPass2 reads them after SessionRunner returns.
func RunPass2(ctx context.Context, opts Pass2Options) error {
	if opts.Backend == nil {
		return fmt.Errorf("scaffold: Pass2Options.Backend is nil")
	}
	if opts.SessionRunner == nil {
		return fmt.Errorf("scaffold: Pass2Options.SessionRunner is nil")
	}
	if opts.ProfileDir == "" {
		return fmt.Errorf("scaffold: Pass2Options.ProfileDir is empty")
	}
	if opts.ClassifiedChapters == nil {
		return fmt.Errorf("scaffold: Pass2Options.ClassifiedChapters is nil")
	}
	if opts.SaveChapterScaffold == nil {
		return fmt.Errorf("scaffold: Pass2Options.SaveChapterScaffold is nil")
	}
	if opts.AppendCompetencies == nil {
		return fmt.Errorf("scaffold: Pass2Options.AppendCompetencies is nil")
	}
	if opts.ListChapterScaffolds == nil {
		return fmt.Errorf("scaffold: Pass2Options.ListChapterScaffolds is nil")
	}

	out := opts.Out
	if out == nil {
		out = os.Stdout
	}

	// Compute resume position.
	alreadyDone, err := opts.ListChapterScaffolds(opts.ProfileDir)
	if err != nil {
		return fmt.Errorf("scaffold: listing chapter scaffolds: %w", err)
	}

	classifications := opts.ClassifiedChapters.Classifications
	startIdx := -1
	for i, cl := range classifications {
		if !alreadyDone[cl.ChapterID] {
			startIdx = i
			break
		}
	}
	if startIdx == -1 {
		// All chapters already scaffolded.
		fmt.Fprintf(out, "Pass 2 already complete — all chapters scaffolded.\n")
		return nil
	}

	// Build a lookup from chapter ID → ingestion.Chapter for locator fallback.
	ingChapterByID := make(map[string]ingestion.Chapter)
	if opts.Ingestion != nil {
		for _, ch := range opts.Ingestion.Chapters {
			ingChapterByID[ch.ID] = ch
		}
	}

	// Closure state.
	var handlerErr error
	var wrapped bool

	// curIdx is the 0-based index into classifications of the chapter
	// currently being discussed. Initialised to startIdx.
	curIdx := startIdx

	// nextHandler: structures the current chapter and advances curIdx.
	nextHandler := func(m tui.Model, _ string) (tui.Model, tea.Cmd) {
		idxAtDispatch := curIdx
		cl := classifications[idxAtDispatch]

		subTranscript := extractSubTranscript(m.History(), cl.ChapterID)
		m, userCmd := m.AppendUserTurn("/next")
		m, sysCmd := m.AppendSystemTurn(fmt.Sprintf("Committing %s — structuring scaffold…", cl.ChapterID))

		cmd := func() tea.Msg {
			scaff, comps, err := StructureChapter(ctx, opts.Backend, cl.ChapterID, cl.Kind, subTranscript)
			if err != nil {
				handlerErr = err
				return tea.QuitMsg{}
			}

			// Apply fallback locator from ingestion if the structurer left it empty.
			if scaff.SourceRef.Locator == "" {
				if ch, ok := ingChapterByID[cl.ChapterID]; ok {
					scaff.SourceRef.Locator = ch.SourceLocator
					if scaff.SourceRef.Type == "" {
						scaff.SourceRef.Type = "book_chapter"
					}
				}
			}

			if err := opts.SaveChapterScaffold(opts.ProfileDir, scaff); err != nil {
				handlerErr = err
				return tea.QuitMsg{}
			}
			if len(comps) > 0 {
				if err := opts.AppendCompetencies(opts.ProfileDir, comps); err != nil {
					handlerErr = err
					return tea.QuitMsg{}
				}
			}

			nextIdx := idxAtDispatch + 1
			curIdx = nextIdx

			if nextIdx >= len(classifications) {
				// All chapters processed.
				return tea.QuitMsg{}
			}

			// Announce the next chapter as a short context message.
			nextCl := classifications[nextIdx]
			nextTitle := nextCl.ChapterID
			if ch, ok := ingChapterByID[nextCl.ChapterID]; ok {
				nextTitle = ch.Title
			}
			return tui.ContextMsg{
				Text:      Pass2ChapterAnnouncement(nextIdx, nextCl.ChapterID, nextTitle, nextCl.Kind),
				AutoReply: true,
			}
		}
		return m, tea.Batch(userCmd, sysCmd, cmd)
	}

	// skipHandler: builds a deferred stub and persists it, then advances.
	skipHandler := func(m tui.Model, _ string) (tui.Model, tea.Cmd) {
		idxAtDispatch := curIdx
		cl := classifications[idxAtDispatch]

		title := cl.ChapterID
		locator := cl.ChapterID
		sourceKind := "book_chapter"
		if ch, ok := ingChapterByID[cl.ChapterID]; ok {
			title = ch.Title
			locator = ch.SourceLocator
		}

		stub := &ChapterScaffold{
			SchemaVersion: CurrentSchemaVersion,
			ID:            cl.ChapterID,
			Title:         title,
			Kind:          cl.Kind,
			SourceRef: SourceRef{
				Type:    sourceKind,
				Locator: locator,
			},
			Deferred: true,
		}

		m, skipUserCmd := m.AppendUserTurn("/skip-chapter")
		m, skipSysCmd := m.AppendSystemTurn(fmt.Sprintf("deferred stub written for %s; moving on.", cl.ChapterID))

		cmd := func() tea.Msg {
			if err := opts.SaveChapterScaffold(opts.ProfileDir, stub); err != nil {
				handlerErr = err
				return tea.QuitMsg{}
			}

			nextIdx := idxAtDispatch + 1
			curIdx = nextIdx

			if nextIdx >= len(classifications) {
				return tea.QuitMsg{}
			}

			// Announce the next chapter as a short context message.
			nextCl := classifications[nextIdx]
			nextTitle := nextCl.ChapterID
			if ch, ok := ingChapterByID[nextCl.ChapterID]; ok {
				nextTitle = ch.Title
			}
			return tui.ContextMsg{
				Text:      Pass2ChapterAnnouncement(nextIdx, nextCl.ChapterID, nextTitle, nextCl.Kind),
				AutoReply: true,
			}
		}
		return m, tea.Batch(skipUserCmd, skipSysCmd, cmd)
	}

	// wrapHandler: exits the session early.
	wrapHandler := func(m tui.Model, _ string) (tui.Model, tea.Cmd) {
		wrapped = true
		m, sysCmd := m.AppendSystemTurn("Pass 2 wrapped early. Re-run to continue from the next unscaffolded chapter.")
		return m, tea.Batch(sysCmd, func() tea.Msg { return tea.QuitMsg{} })
	}

	systemPrompt := fmt.Sprintf("Chapter %d: %s (%s)", startIdx+1, classifications[startIdx].ChapterID, classifications[startIdx].Kind)
	if opts.Goals != nil && opts.StartingPoint != nil && opts.Recommendation != nil && opts.Ingestion != nil && opts.ClassifiedChapters != nil {
		systemPrompt = Pass2SystemPrompt(opts.Goals, opts.StartingPoint, opts.Recommendation, opts.Ingestion, opts.ClassifiedChapters, startIdx)
	}

	tuiOpts := tui.Options{
		Backend:         opts.Backend,
		SystemPrompt:    systemPrompt,
		HeaderText:      "Lernen Forge — Stage 4 Pass 2: Scaffolding",
		ModelLabel:      opts.ModelLabel,
		DispatchCtx:     ctx,
		IntroMessage:    "Walk each chapter with the mentor. /next to scaffold and advance, /skip-chapter to defer, /wrap to stop early.",
		DisableFirewall: true,
		SlashHandlers: map[string]tui.SlashHandler{
			"next":          nextHandler,
			"skip-chapter":  skipHandler,
			"wrap":          wrapHandler,
		},
		SlashHandlerHelp: map[string]string{
			"next":         "Structure the current chapter and advance to the next",
			"skip-chapter": "Write a deferred stub for the current chapter and advance",
			"wrap":         "Exit Pass 2 early; resume later from the first unscaffolded chapter",
		},
	}

	if err := opts.SessionRunner(tuiOpts); err != nil {
		return fmt.Errorf("scaffold: session runner: %w", err)
	}
	if handlerErr != nil {
		return handlerErr
	}

	if wrapped {
		fmt.Fprintf(out, "Pass 2 wrapped early.\n")
	} else {
		fmt.Fprintf(out, "Pass 2 complete.\n")
	}
	return nil
}

// extractSubTranscript returns the portion of the conversation history that
// pertains to the given chapter. It finds the last RoleContext turn whose
// content contains chapterID (the turn injected when the chapter started)
// and returns all turns from that point onward, rendered with role prefixes.
// If no such anchor is found, the full transcript is returned.
func extractSubTranscript(turns []tui.Turn, chapterID string) string {
	anchorIdx := -1
	for i, t := range turns {
		if t.Role == tui.RoleContext && strings.Contains(t.Content, chapterID) {
			anchorIdx = i
		}
	}
	if anchorIdx >= 0 {
		turns = turns[anchorIdx:]
	}

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
