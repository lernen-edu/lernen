// Package tui implements the Bubble Tea-driven chat interface (PRD §4.1)
// that hosts a Phase 1 tutor session. The TUI's load-bearing job is to
// honor the no-flicker invariant from PRD §4.1 and PRE_BUILD_ANSWERS §3:
// the body of an open code block — fenced or indented — must never reach
// the screen until the firewall has confirmed it is at most three lines.
//
// Implementation: a phase1.Streamer mediates between the Backend's token
// channel and the rendered tutor turn. Tokens flow Write → safe-suffix →
// pending.content; the Streamer holds code-block bodies until the
// boundary is known. View() renders only the in-flight pending turn (if
// any) + bordered input + status line. Finalized turns flow to terminal
// scrollback via Printer.Println at each append-time site.
package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"

	"github.com/lernen-edu/lernen/internal/backends"
	"github.com/lernen-edu/lernen/internal/curriculum"
	"github.com/lernen-edu/lernen/internal/phase1"
)

// cancelledMarker is appended on its own line to the partial pending
// turn when the user presses Esc mid-stream and there is non-empty
// content to commit. Plain text, no Unicode, matches the convention of
// phase1.FirewallMarker.
const cancelledMarker = "[cancelled]"

// Printer abstracts the "emit a line to terminal scrollback" channel.
// Production uses teaPrinter which wraps tea.Println; tests inject a
// capturePrinter that records lines into an internal slice so assertions
// can run without a real Bubble Tea program.
type Printer interface {
	Println(args ...any) tea.Cmd
}

// teaPrinter is the production implementation: defer to Bubble Tea's
// tea.Println, which the runtime flushes to stdout ABOVE the rendered
// region so the conversation flows into the terminal's native scrollback.
type teaPrinter struct{}

// Println returns a tea.Cmd that emits args followed by a newline above
// the rendered region.
func (teaPrinter) Println(args ...any) tea.Cmd { return tea.Println(args...) }

// capturePrinter is the test implementation: append formatted lines to
// an internal slice so tests can assert against captured output without
// running a real Bubble Tea program.
type capturePrinter struct {
	lines []string
}

// Println formats args (fmt.Sprint rules) and appends as one line to
// the capture slice. Returns a no-op tea.Cmd so callers can pass the
// result through tea.Batch identically to production.
func (c *capturePrinter) Println(args ...any) tea.Cmd {
	c.lines = append(c.lines, fmt.Sprint(args...))
	return func() tea.Msg { return nil }
}

// maxInputHeight caps how many vertical rows the textarea can grow to,
// counting both logical lines (from Shift+Enter / Ctrl+J / Alt+Enter
// inserts) and visual rows from word-wrap of long single lines. Beyond
// this the textarea scrolls internally rather than stealing more screen
// space from the conversation viewport.
const maxInputHeight = 5

// SlashHandler is the signature of a slash-command extension handler.
// The handler receives the current Model and the args portion of the
// command (everything after the first whitespace, already trimmed).
// It returns the (possibly mutated) Model and an optional tea.Cmd.
//
// Contract: handlers own history and emit. Specifically, the
// dispatch path does NOT append a roleUser turn for the input that
// triggered the handler. If the handler wants the typed command to
// appear in the transcript, it must call m.publishTurn (or the
// exported m.AppendUserTurn) — which appends to history AND emits
// the rendered line through the Printer so the line appears in
// terminal scrollback. This freedom lets, e.g., /wrap suppress the
// echo and emit the structuring result directly.
type SlashHandler func(m Model, args string) (Model, tea.Cmd)

// Options configures a Model. Backend and SystemPrompt are required;
// Curriculum and Chapter are optional and used only for the header.
type Options struct {
	Backend      backends.Backend
	SystemPrompt string
	Curriculum   *curriculum.Curriculum
	Chapter      *curriculum.Chapter

	// ModelLabel is the human-readable identifier for the active backend
	// + model, surfaced in the status bar (e.g., "openrouter/openai/gpt-5.4").
	// Optional — when empty the status bar omits the model segment.
	ModelLabel string

	// HeaderText, when non-empty, is rendered as the sticky header line
	// in place of the curriculum/chapter banner. Used by the forge,
	// which has no curriculum at the time of Stage 0 but needs a
	// stage-identifying banner. When empty, Curriculum+Chapter rendering
	// applies (existing behavior).
	HeaderText string

	// IntroMessage, when non-empty, is pre-seeded into history as a
	// single RoleSystem turn so the user has mode-specific guidance on
	// first paint without having to send a tokens-wasting "hi" or
	// "what do I do" prompt. RoleSystem turns are dropped by both
	// buildBackendMessages and the forge's extractTranscript, so the
	// intro is purely cosmetic — it never leaks into the model context
	// or the structured YAML. The intro persists in m.history for the duration of the session.
	IntroMessage string

	// DisableFirewall, when true, swaps the Phase 1 firewall streamer
	// (which truncates code blocks longer than 3 lines and would prevent
	// the M1 tutor from solving problems for the student) for a passthrough
	// that emits bytes verbatim. Forge stages set this to true per
	// M3a/b/c spec §2 — the firewall applies to forge OUTPUTS (M3e
	// exercise generation), not to the forge's own mentor dialogue
	// where small calibration code probes are appropriate. Default
	// false preserves the load-bearing M1 work-mode pedagogy invariant.
	DisableFirewall bool

	// SlashHandlers registers extra slash commands beyond the built-in
	// set (/help, /quit, /copy). Built-ins always
	// take precedence; SlashHandlers fires only for commands that
	// parseSlashCommand returns slashUnknown for. The forge registers
	// /wrap here. Map keys are lowercase command names (no leading
	// slash).
	SlashHandlers map[string]SlashHandler

	// SlashHandlerHelp provides one-line descriptions for the
	// SlashHandlers entries, surfaced in /help. Map keys must match
	// SlashHandlers keys; missing entries default to "(no description)".
	SlashHandlerHelp map[string]string

	// ExplainBackGate, when non-nil, intercepts every non-slash user
	// turn before backend dispatch. It returns a Cmd that resolves to
	// GatePassMsg (proceed to dispatch) or GateHoldMsg (post the
	// follow-up, return to input). nil ⇒ legacy direct-dispatch path,
	// so the forge TUI and --training-wheels-off are unaffected. The
	// closure is constructed in cli/work.go and must itself fail open
	// (return GatePassMsg) on any evaluator error.
	ExplainBackGate func(pendingTurn, recentTranscript string) tea.Cmd

	// DispatchCtx is the context handed to backend.StreamChat. Defaults
	// to context.Background when zero.
	DispatchCtx context.Context

	// Printer is the channel by which the Model emits finalized turns
	// to terminal scrollback (via tea.Println). Defaults to a teaPrinter
	// shim when zero; tests inject a *capturePrinter to assert.
	Printer Printer
}

// Model is the Bubble Tea model holding all session state.
type Model struct {
	opts   Options
	styles *Styles

	input   textarea.Model
	spinner spinner.Model
	streamer *phase1.Streamer
	// newStreamer constructs a fresh streamer for each new turn. Set
	// once in New() based on Options.DisableFirewall so the per-turn
	// reset sites (handleSubmit, streamDoneMsg, streamErrMsg) don't
	// have to know which mode the model is in.
	newStreamer func() *phase1.Streamer

	// mdRenderer formats roleTutor turns as styled markdown (bold,
	// italic, lists, inline code, fenced ≤3-line code blocks with
	// syntax highlighting). Configured per WindowSizeMsg with the
	// correct word-wrap width. Nil when the renderer init fails;
	// renderTurn falls back to plain text in that case.
	mdRenderer *glamour.TermRenderer

	history []Turn // finalized turns
	pending *Turn  // in-progress tutor turn; nil when not streaming
	tokenCh <-chan backends.Token

	width   int
	waiting bool

	violations []phase1.Violation

	// cancelStream is the per-stream cancel function set in handleSubmit
	// and cleared in streamDoneMsg / streamErrMsg. Pressing Esc while
	// waiting calls this; nil when no stream is in flight.
	cancelStream context.CancelFunc

	// quitting is set when /quit (or Ctrl+C) is received. View renders
	// nothing in this state so the terminal cleans up cleanly.
	quitting bool

	// introPrinted gates the one-time emission of RoleSystem turns
	// pre-seeded by New() (the IntroMessage). The emission is deferred
	// from Init() to the first WindowSizeMsg so renderTurn has the
	// real terminal width — otherwise contentWidth() floors at 20 and
	// the body wraps to ~12 columns, producing a vertical-line artifact.
	introPrinted bool

	// inputHistory is the append-only ring of past user submissions
	// (slash commands included). Up arrow at the first textarea line
	// walks backward through it; Down arrow at the last line walks
	// forward. The current navigation position is inputHistoryIdx;
	// when idx == len(inputHistory) the user is in "normal" composition
	// mode (not navigating); inputDraft holds whatever was being typed
	// before navigation began so it can be restored on the way out.
	inputHistory    []string
	inputHistoryIdx int
	inputDraft      string

	// reverse-incremental search state. When searchMode is true the
	// outer KeyMsg handler routes every keystroke through
	// updateSearchMode rather than the normal switch — ALL input goes
	// to the search overlay until Enter (commit), Esc / Ctrl+G
	// (cancel), or any other terminating key fires. The input
	// textarea is repurposed to display the current match (preview);
	// the status bar shows the search query in bash readline style.
	searchMode     bool
	searchQuery    string
	searchMatchIdx int    // -1 = no match found
	preSearchValue string // textarea value before search began; restored on cancel
}

// History returns the finalized turns in conversation order. Used by
// callers (e.g., the forge's /wrap handler) that need to inspect the
// post-conversation transcript without touching internal fields.
// The returned slice is a copy — mutating it does not affect Model state.
func (m Model) History() []Turn {
	out := make([]Turn, len(m.history))
	copy(out, m.history)
	return out
}

// AppendSystemTurn appends a system-role turn to the conversation
// history AND emits the rendered line through the Printer so it
// appears in terminal scrollback. Returns the mutated Model and the
// Println command (callers tea.Batch it with any other commands they're
// returning).
func (m Model) AppendSystemTurn(text string) (Model, tea.Cmd) {
	t := Turn{Role: RoleSystem, Content: text}
	return m.publishTurn(t)
}

// AppendUserTurn appends a user-role turn AND emits the rendered line
// through the Printer. Mirrors AppendSystemTurn's contract.
func (m Model) AppendUserTurn(text string) (Model, tea.Cmd) {
	t := Turn{Role: RoleUser, Content: text}
	return m.publishTurn(t)
}

// SetWaiting sets the waiting flag. When waiting is true, Enter
// submissions are no-ops and the status bar shows the "thinking" /
// cancel-stream affordance. SlashHandler implementations that
// dispatch a long-running tea.Cmd (e.g., the forge's /wrap handler
// running a structuring call) should SetWaiting(true) before
// returning the cmd; the cmd's tea.Msg result, when handled by
// Update, will SetWaiting(false) (or trigger a quit/cancel that
// clears it).
func (m Model) SetWaiting(waiting bool) Model {
	m.waiting = waiting
	return m
}

// SystemMsg is a tea.Msg that, when received, appends Text as a
// system-role turn. Callers (slash-handler tea.Cmds) emit this from
// cmd closures so the message flows through Update naturally and the
// viewport refreshes automatically.
//
// SystemMsg turns are UI-only — they do NOT reach the backend in
// the conversation context. For system content the model must see
// (e.g., extraction results that the mentor must reason over), emit
// ContextMsg instead.
type SystemMsg struct{ Text string }

// ContextMsg is a tea.Msg that appends Text as a context-role turn.
// Visually identical to SystemMsg (renders under the "system" label),
// but the resulting Turn IS forwarded to the backend as a system
// message in the conversation context — so the model sees the
// content. Use for runtime extraction results, async tool output,
// or anything else the mentor must reason over.
//
// When AutoReply is false (the default), receipt of ContextMsg clears
// the waiting flag — slash handlers that SetWaiting(true) before
// dispatching async work can rely on the eventual ContextMsg to release
// the input gate when the work completes. When AutoReply is true,
// waiting is immediately re-set by startStream.
//
// AutoReply, when true, dispatches a tutor stream against the just-
// updated history immediately after appending the context turn. Use
// this when the context is a transition signal that the mentor should
// react to without waiting for the user to prompt — e.g., a Pass 2
// /next handler announcing the next chapter. Default false matches
// historical behavior (the user looks at the context and prompts the
// mentor themselves).
type ContextMsg struct {
	Text      string
	AutoReply bool
}

// QuitWithMessage is a tea.Msg that appends Text as a system turn,
// then quits the program. The Update handler batches a tea.Println
// command (via publishTurn) alongside the tea.Quit so the system
// turn lands in terminal scrollback before the runtime unwinds.
type QuitWithMessage struct{ Text string }

// GatePassMsg signals the explain-back gate allowed the pending turn;
// the model proceeds to backend dispatch.
type GatePassMsg struct{}

// GateHoldMsg signals the gate held the turn; Followup is shown to the
// learner as a system turn and the model returns to input.
type GateHoldMsg struct{ Followup string }

// New constructs a fresh Model. The textarea is focused immediately so
// the user can start typing the first message.
func New(opts Options) Model {
	if opts.DispatchCtx == nil {
		opts.DispatchCtx = context.Background()
	}
	if opts.Printer == nil {
		opts.Printer = teaPrinter{}
	}

	styles := DefaultStyles()

	ta := textarea.New()
	// Placeholder intentionally empty: textarea v1's placeholderView
	// (textarea.go:1271–1280) overlays the cursor on the first char of
	// the placeholder and doesn't pad the cursor line to the full input
	// width. The result is a blinking lowercase letter and a dim color
	// that stops halfway across the box. The `›` prompt and the bordered
	// input region already indicate where to type; /help is documented
	// in the status line.
	ta.Prompt = "› "
	ta.CharLimit = 4096
	ta.MaxHeight = maxInputHeight
	ta.SetHeight(1)
	ta.SetWidth(78) // outer 80 minus 2 border cells; updated on first WindowSizeMsg
	ta.ShowLineNumbers = false
	_ = ta.Focus() // discard the cursor-focus cmd; Init returns textarea.Blink

	sp := spinner.New(
		spinner.WithSpinner(spinner.Dot),
		spinner.WithStyle(styles.Spinner),
	)

	newStreamer := phase1.NewStreamer
	if opts.DisableFirewall {
		newStreamer = phase1.NewPassthroughStreamer
	}
	m := Model{
		opts:    opts,
		styles:  styles,
		input:   ta,
		spinner: sp,
		streamer:    newStreamer(),
		newStreamer: newStreamer,
	}
		if intro := strings.TrimSpace(opts.IntroMessage); intro != "" {
		m.history = append(m.history, Turn{Role: RoleSystem, Content: intro})
	}
	return m
}

// Init starts the textarea's cursor blink ticker and the spinner's
// animation tick. The pre-seeded RoleSystem intro turns are NOT
// emitted here — renderTurn does width-aware word-wrapping, and
// m.width is 0 until the first WindowSizeMsg arrives. The intro is
// emitted from the WindowSizeMsg handler (gated by m.introPrinted)
// so the wrap uses the real terminal width. Session identity is
// carried by the always-visible status line; no separate header
// banner is rendered.
func (m Model) Init() tea.Cmd {
	return tea.Batch(textarea.Blink, m.spinner.Tick)
}

// Update is the message-dispatch heart of the TUI.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		inputInnerWidth := m.contentWidth() - 2
		if inputInnerWidth < 8 {
			inputInnerWidth = 8
		}
		m.input.SetWidth(inputInnerWidth)
		m.mdRenderer = newMarkdownRenderer(m.contentWidth() - 8)

		// First WindowSizeMsg: emit the pre-seeded RoleSystem turns
		// (IntroMessage) now that we have a real terminal width for
		// renderTurn's word-wrap. Subsequent resizes are a no-op for
		// the intro — it's already in scrollback.
		var introCmds []tea.Cmd
		if !m.introPrinted && m.opts.Printer != nil {
			for _, t := range m.history {
				if t.Role == RoleSystem {
					introCmds = append(introCmds, m.opts.Printer.Println(renderTurn(t, m.contentWidth(), m.styles, m.mdRenderer)))
				}
			}
			m.introPrinted = true
		}
		if len(introCmds) == 0 {
			return m, nil
		}
		return m, tea.Batch(introCmds...)

	case tea.KeyMsg:
		// Reverse-search mode owns every keystroke: typing extends the
		// query, Backspace shortens it, Ctrl+R steps to the next older
		// match, Enter commits, Esc / Ctrl+G cancels and restores the
		// pre-search input. Routing through updateSearchMode keeps the
		// search overlay state machine self-contained.
		if m.searchMode {
			return m.updateSearchMode(msg)
		}
		switch msg.String() {
		case "ctrl+c":
			m.quitting = true
			return m, tea.Quit
		case "esc":
			// Cancel an in-flight stream. The backend's StreamChat goroutine
			// receives ctx.Done(), emits Token{Err: context.Canceled}, and
			// closes the channel; waitForToken then resolves to streamErrMsg
			// which the cancellation-aware handler below treats specially.
			//
			// When not waiting, Esc falls through to textarea (whose default
			// keymap does not bind it), making this a no-op outside a stream.
			if m.waiting && m.cancelStream != nil {
				m.cancelStream()
				return m, nil
			}
		case "enter":
			if m.waiting {
				return m, nil
			}
			text := strings.TrimSpace(m.input.Value())
			m.input.Reset()
			if text == "" {
				m = m.syncInputHeight()
				return m, nil
			}
			// Record the submission into the input-history ring before
			// dispatching, and reset history-nav state so the next Up
			// arrow walks back from the newest entry rather than from
			// wherever a prior recall left the cursor.
			m.inputHistory = append(m.inputHistory, text)
			m.inputHistoryIdx = len(m.inputHistory)
			m.inputDraft = ""
			m = m.syncInputHeight()
			return m, func() tea.Msg { return submitMsg{text: text} }
		case "ctrl+u":
			// Kill the input line — clear the textarea without submitting.
			// Cancels any in-progress history navigation too.
			if m.waiting {
				return m, nil
			}
			m.input.Reset()
			m.inputHistoryIdx = len(m.inputHistory)
			m.inputDraft = ""
			m = m.syncInputHeight()
			return m, nil
		case "ctrl+r":
			// Enter reverse-incremental search mode. No-op while
			// waiting (mid-stream input is suppressed) and no-op when
			// there's nothing to search through.
			if m.waiting || len(m.inputHistory) == 0 {
				return m, nil
			}
			m.searchMode = true
			m.searchQuery = ""
			m.searchMatchIdx = -1
			m.preSearchValue = m.input.Value()
			return m, nil
		case "up":
			// History recall — only at the first line of the textarea.
			// Anywhere else, fall through so textarea handles cursor
			// movement within multi-line input.
			if !m.waiting && m.input.Line() == 0 && len(m.inputHistory) > 0 && m.inputHistoryIdx > 0 {
				if m.inputHistoryIdx == len(m.inputHistory) {
					m.inputDraft = m.input.Value()
				}
				m.inputHistoryIdx--
				m = m.replaceInputWith(m.inputHistory[m.inputHistoryIdx])
				return m, nil
			}
		case "down":
			// History forward — only at the last line of the textarea.
			// At the newest entry, restore whatever the user was typing
			// before they started navigating (the saved draft).
			if !m.waiting && m.input.Line() == m.input.LineCount()-1 && m.inputHistoryIdx < len(m.inputHistory) {
				m.inputHistoryIdx++
				if m.inputHistoryIdx == len(m.inputHistory) {
					m = m.replaceInputWith(m.inputDraft)
					m.inputDraft = ""
				} else {
					m = m.replaceInputWith(m.inputHistory[m.inputHistoryIdx])
				}
				return m, nil
			}
		case "alt+enter", "ctrl+j":
			// Insert a literal newline into the textarea. Bubble Tea v1
			// has no built-in support for parsing Shift+Enter from the
			// terminal — it requires kitty-keyboard / modifyOtherKeys
			// protocol handshakes that v1 doesn't perform — so we bind
			// the two combinations the v1 runtime actually delivers:
			//   alt+enter — Esc-prefix + CR; works in most terminals
			//               with Option-as-Meta enabled (iTerm2 default,
			//               Terminal.app needs profile setting).
			//   ctrl+j    — emits LF (0x0a); universal fallback,
			//               works on every terminal Bubble Tea has
			//               ever supported.
			if m.waiting {
				return m, nil
			}
			m.input.InsertString("\n")
			m = m.syncInputHeight()
			return m, nil
		}
		// fall through — non-special key: viewport / textarea will receive it below

	case submitMsg:
		return m.handleSubmit(msg.text)

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case clipboardResultMsg:
		var line string
		if msg.err != nil {
			line = fmt.Sprintf("copy failed: %s", msg.err)
		} else {
			line = "copied tutor's last reply to clipboard."
		}
		m, cmd := m.publishTurn(Turn{Role: RoleSystem, Content: line})
		return m, cmd

	case streamReadyMsg:
		m.tokenCh = msg.ch
		return m, waitForToken(msg.ch)

	case tokenMsg:
		if m.pending != nil {
			out, vs := m.streamer.Write([]byte(msg.text))
			m.pending.Content += out
			m.violations = append(m.violations, vs...)
		}
		if m.tokenCh == nil {
			return m, nil
		}
		return m, waitForToken(m.tokenCh)

	case streamDoneMsg:
		out, vs := m.streamer.Flush()
		var cmd tea.Cmd
		if m.pending != nil {
			m.pending.Content += out
			// Publish the finalized tutor turn to scrollback.
			m, cmd = m.publishTurn(*m.pending)
			m.pending = nil
		}
		m.violations = append(m.violations, vs...)
		m.streamer = m.newStreamer()
		m.tokenCh = nil
		m.waiting = false
		m = m.clearCancel()
		return m, cmd

	case streamErrMsg:
		// User-cancellation path: the partial pending turn — if it has
		// any content — is committed to history with a [cancelled]
		// marker so the user can see what they interrupted. An empty
		// pending turn is dropped entirely so the history stays clean.
		// No "backend error: ..." surface for cancellations.
		if errors.Is(msg.err, context.Canceled) {
			out, vs := m.streamer.Flush()
			var cmd tea.Cmd
			if m.pending != nil {
				m.pending.Content += out
				if strings.TrimSpace(m.pending.Content) != "" {
					m.pending.Content = strings.TrimRight(m.pending.Content, "\n") + "\n\n" + cancelledMarker
					m, cmd = m.publishTurn(*m.pending)
				}
				m.pending = nil
			}
			m.violations = append(m.violations, vs...)
			m.streamer = m.newStreamer()
			m.tokenCh = nil
			m.waiting = false
			m = m.clearCancel()
			return m, cmd
		}

		// Real error path: drop partial content and surface a system turn.
		m.pending = nil
		m.streamer = m.newStreamer()
		m.tokenCh = nil
		m.waiting = false
		m = m.clearCancel()
		m, cmd := m.publishTurn(Turn{Role: RoleSystem, Content: fmt.Sprintf("backend error: %s", msg.err)})
		return m, cmd

	case SystemMsg:
		m, cmd := m.publishTurn(Turn{Role: RoleSystem, Content: msg.Text})
		return m, cmd

	case ContextMsg:
		m, ctxCmd := m.publishTurn(Turn{Role: RoleContext, Content: msg.Text})
		if msg.AutoReply {
			// Don't clear m.waiting; startStream will set it for the auto dispatch.
			m, startCmd := m.startStream()
			return m, tea.Batch(ctxCmd, startCmd)
		}
		m.waiting = false
		return m, ctxCmd

	case GatePassMsg:
		m, cmd := m.startStream()
		return m, cmd

	case GateHoldMsg:
		m.waiting = false
		m, cmd := m.AppendSystemTurn(msg.Followup)
		return m, cmd

	case QuitWithMessage:
		m, cmd := m.publishTurn(Turn{Role: RoleSystem, Content: msg.Text})
		m.quitting = true
		return m, tea.Batch(cmd, tea.Quit)
	}

	// Forward unhandled messages to the textarea, which handles cursor
	// movement, insertion, etc. The viewport is gone — terminal scrollback
	// owns the conversation history view.
	var inCmd tea.Cmd
	if !m.waiting {
		m.input, inCmd = m.input.Update(msg)
		m = m.syncInputHeight()
	}
	return m, inCmd
}

// handleSubmit runs after Enter has produced a submitMsg. It dispatches
// either a slash command (locally) or a backend message (via dispatchStream).
func (m Model) handleSubmit(text string) (tea.Model, tea.Cmd) {
	cmd, args := parseSlashCommand(text)
	switch cmd {
	case slashQuit:
		m.quitting = true
		return m, tea.Quit
	case slashHelp:
		m, userCmd := m.publishTurn(Turn{Role: RoleUser, Content: text})
		m, helpCmd := m.publishTurn(Turn{Role: RoleSystem, Content: helpText(m.opts.SlashHandlerHelp)})
		return m, tea.Batch(userCmd, helpCmd)
	case slashCopy:
		// Copy the most recent tutor turn's content to the OS clipboard.
		// User input is the user's own text, copyable via terminal-native
		// selection (Option-drag on macOS), so /copy targets the tutor side.
		last := lastTutorTurn(m.history)
		m, userCmd := m.publishTurn(Turn{Role: RoleUser, Content: text})
		if last == "" {
			m, sysCmd := m.publishTurn(Turn{
				Role:    RoleSystem,
				Content: "no tutor reply yet — nothing to copy.",
			})
			return m, tea.Batch(userCmd, sysCmd)
		}
		return m, tea.Batch(userCmd, copyClipboardCmd(m.opts.DispatchCtx, last))
	case slashHint:
		m, userCmd := m.publishTurn(Turn{Role: RoleUser, Content: text})
		m, hintCmd := m.publishTurn(Turn{Role: RoleSystem, Content: hintText})
		return m, tea.Batch(userCmd, hintCmd)
	case slashUnknown:
		if name := slashCommandName(text); name != "" {
			if h, ok := m.opts.SlashHandlers[name]; ok {
				return h(m, args)
			}
		}
		m, userCmd := m.publishTurn(Turn{Role: RoleUser, Content: text})
		m, unkCmd := m.publishTurn(Turn{
			Role:    RoleSystem,
			Content: fmt.Sprintf("unknown command: %q. Try /help.", text),
		})
		return m, tea.Batch(userCmd, unkCmd)
	}

	// slashNone: gate (if configured) then dispatch to backend.
	m, userCmd := m.publishTurn(Turn{Role: RoleUser, Content: text})
	if m.opts.ExplainBackGate != nil {
		m.waiting = true
		// history WITHOUT the just-published user turn — that text is
		// passed separately as pendingTurn, so excluding it here keeps
		// the gate from seeing the pending message twice.
		gateCmd := m.opts.ExplainBackGate(text, recentTranscript(m.history[:len(m.history)-1], 8))
		return m, tea.Batch(userCmd, gateCmd)
	}
	m, startCmd := m.startStream()
	return m, tea.Batch(userCmd, startCmd)
}

// startStream initiates a backend dispatch against the current
// m.history. It sets up the pending tutor turn, waiting flag, fresh
// streamer state, and cancelable context, then returns the dispatch
// cmd. Callers must have appended any new turns to history first if
// appropriate (handleSubmit's slashNone branch appends a RoleUser turn
// before calling; the ContextMsg AutoReply path appends a RoleContext
// turn before calling; neither appends both).
func (m Model) startStream() (Model, tea.Cmd) {
	m.pending = &Turn{Role: RoleTutor, Content: ""}
	m.waiting = true
	m.streamer = m.newStreamer()

	streamCtx, cancel := context.WithCancel(m.opts.DispatchCtx)
	m.cancelStream = cancel

	msgs := buildBackendMessages(m.history)
	return m, dispatchStream(streamCtx, m.opts.Backend, msgs, m.opts.SystemPrompt)
}

// clearCancel releases the per-stream cancel function. Calling cancel after
// the stream has finished is a no-op for the goroutine but it does free the
// context's internal goroutine and timer state — the standard library
// requires this. Returning Model rather than mutating in place keeps the
// rest of Update's value-receiver pattern consistent.
func (m Model) clearCancel() Model {
	if m.cancelStream != nil {
		m.cancelStream()
		m.cancelStream = nil
	}
	return m
}

// syncInputHeight resizes the textarea to fit current content (clamped
// by maxInputHeight). Called after every state change that could shift
// line count: the fall-through KeyMsg path, Reset on submit, InsertString
// from Shift+Enter / Ctrl+J / Alt+Enter.
//
// Height is computed from visual rows, not logical newlines: a long
// line that wraps in the textarea contributes multiple rows even with
// no embedded "\n". The wrap width is the textarea's inner area minus
// the prompt width (textarea draws Prompt at the start of every line).
func (m Model) syncInputHeight() Model {
	wrapWidth := m.input.Width() - lipgloss.Width(m.input.Prompt)
	if wrapWidth < 1 {
		wrapWidth = 1
	}
	desired := 0
	for _, line := range strings.Split(m.input.Value(), "\n") {
		w := lipgloss.Width(line)
		if w == 0 {
			desired++
			continue
		}
		desired += (w + wrapWidth - 1) / wrapWidth
	}
	if desired < 1 {
		desired = 1
	}
	if desired > maxInputHeight {
		desired = maxInputHeight
	}
	if desired != m.input.Height() {
		m.input.SetHeight(desired)
	}
	return m
}

// newMarkdownRenderer constructs a glamour TermRenderer with the dark
// stylesheet and the supplied word-wrap width. Returns nil on init
// error — callers (renderTurn) treat nil as "render plain text" so a
// renderer-init failure can never crash the TUI mid-session.
func newMarkdownRenderer(wrap int) *glamour.TermRenderer {
	if wrap < 20 {
		wrap = 20
	}
	r, err := glamour.NewTermRenderer(
		glamour.WithStandardStyle("dark"),
		glamour.WithWordWrap(wrap),
	)
	if err != nil {
		return nil
	}
	return r
}


// View renders the bottom-pinned region returned to the Bubble Tea
// runtime. In inline mode this is the only on-screen real estate the
// program owns; finalized conversation turns flow into terminal
// scrollback via Printer.Println at append-time.
//
// Layout: optional in-flight pending turn, then a blank padding row,
// then the bordered input, then the status line.
func (m Model) View() string {
	if m.quitting {
		return ""
	}
	if m.width == 0 {
		// First-paint guard: WindowSizeMsg has not arrived yet.
		return ""
	}
	var sb strings.Builder
	if m.pending != nil && m.pending.Content != "" {
		sb.WriteString(renderTurn(*m.pending, m.contentWidth(), m.styles, m.mdRenderer))
		sb.WriteString("\n\n")
	}
	sb.WriteString(m.renderInput())
	if status := m.renderStatus(); status != "" {
		sb.WriteString("\n")
		sb.WriteString(status)
	}
	return sb.String()
}

// renderInput wraps the textarea View in a rounded border whose color
// reflects the input's focus state. While waiting on a stream the input
// is blurred (suppressed) — the dimmed border telegraphs that to the
// user without removing the input area entirely from the layout.
func (m Model) renderInput() string {
	style := m.styles.InputBorderFocus
	if m.waiting || !m.input.Focused() {
		style = m.styles.InputBorderBlur
	}
	return style.
		Border(lipgloss.RoundedBorder()).
		Width(m.contentWidth()).
		Render(m.input.View())
}

// contentWidth returns the width budget for renderTurn, leaving a small
// margin so wrapped output doesn't crowd the terminal edge.
func (m Model) contentWidth() int {
	w := m.width - 2
	if w < 20 {
		w = 20
	}
	return w
}

// renderStatus produces the status line shown below the input. In
// inline rendering mode the status line is the single always-visible
// surface for session identity — there is no header banner. Idle
// content: curriculum-id · chapter-shorthand · model · key bindings
// (for work mode); HeaderText · model · key bindings (for forge
// stages that have no curriculum). Streaming swaps the identity
// segments for spinner + "thinking" + model + esc-to-cancel. Search
// mode (Ctrl+R) shows the bash-readline overlay.
func (m Model) renderStatus() string {
	if m.searchMode {
		prefix := "(reverse-i-search)"
		if m.searchQuery != "" && m.searchMatchIdx < 0 {
			prefix = "(failing reverse-i-search)"
		}
		return " " + m.styles.Status.Render(fmt.Sprintf("%s`%s': enter to commit, esc to cancel", prefix, m.searchQuery))
	}

	var segments []string

	if m.waiting {
		segments = append(segments, m.spinner.View()+" thinking")
		if m.opts.ModelLabel != "" {
			segments = append(segments, m.opts.ModelLabel)
		}
		segments = append(segments, "esc to cancel")
	} else {
		// Idle. Curriculum + chapter for work mode; HeaderText as fallback
		// for forge stages that don't carry a curriculum.
		if m.opts.Curriculum != nil {
			segments = append(segments, m.opts.Curriculum.Metadata.ID)
			if m.opts.Chapter != nil {
				if shorthand := chapterShorthand(m.opts.Chapter); shorthand != "" {
					segments = append(segments, shorthand)
				}
			}
		} else if text := strings.TrimSpace(m.opts.HeaderText); text != "" {
			segments = append(segments, text)
		}
		if m.opts.ModelLabel != "" {
			segments = append(segments, m.opts.ModelLabel)
		}
		segments = append(segments, "enter send · alt+enter newline · /help · ctrl+c quit")
	}

	return " " + m.styles.Status.Render(strings.Join(segments, " · "))
}

// chapterShorthand returns "<short-id>: <title>" for the status line.
// The canonical manifest chapter id is e.g. "pcc-ch02"; the status
// surfaces "ch02" as a short suffix to keep the line compact. When
// the id doesn't contain a recognizable "-ch" segment, the full id
// is used.
func chapterShorthand(c *curriculum.Chapter) string {
	if c == nil {
		return ""
	}
	id := c.ID
	if idx := strings.Index(c.ID, "-ch"); idx >= 0 && idx+3 < len(c.ID) {
		rest := c.ID[idx+1:]
		if dash := strings.Index(rest, "-"); dash >= 0 {
			id = rest[:dash]
		} else {
			id = rest
		}
	}
	if c.Title == "" {
		return id
	}
	return id + ": " + c.Title
}

// updateSearchMode owns keyboard input while reverse-incremental search
// is active. The bash-readline contract:
//   - printable runes extend the query and re-search from the newest entry
//   - backspace pops the query and re-searches
//   - Ctrl+R cycles to the next older match (skip current matchIdx)
//   - Enter commits whatever the textarea currently shows (the match) and
//     exits search mode
//   - Esc / Ctrl+G discards the search, restores preSearchValue, exits
//   - Any other key (arrows, ctrl+a, etc.) commits the current match and
//     forwards the original event so the user can keep editing or run
//     another command
func (m Model) updateSearchMode(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+g":
		m = m.exitSearchMode(false)
		return m, nil
	case "enter":
		// Commit: textarea already shows the match (or the empty
		// post-Backspace state); just leave search mode and let the
		// user submit or continue editing.
		m = m.exitSearchMode(true)
		return m, nil
	case "ctrl+r":
		// Step to the next older match.
		if m.searchQuery == "" {
			return m, nil
		}
		next := m.findHistoryMatch(m.searchQuery, m.searchMatchIdx-1)
		if next >= 0 {
			m.searchMatchIdx = next
			m = m.replaceInputWith(m.inputHistory[next])
		}
		return m, nil
	case "backspace":
		if m.searchQuery == "" {
			return m, nil
		}
		m.searchQuery = m.searchQuery[:len(m.searchQuery)-1]
		m = m.refreshSearchMatch()
		return m, nil
	case "ctrl+c":
		m.quitting = true
		return m, tea.Quit
	}

	// Printable input extends the query.
	if msg.Type == tea.KeyRunes && len(msg.Runes) > 0 {
		m.searchQuery += string(msg.Runes)
		m = m.refreshSearchMatch()
		return m, nil
	}
	if msg.Type == tea.KeySpace {
		m.searchQuery += " "
		m = m.refreshSearchMatch()
		return m, nil
	}

	// Unrecognized special key: commit the match and forward the key
	// to normal handling so the user's intent (e.g. arrow navigation
	// of the recalled entry) lands.
	m = m.exitSearchMode(true)
	return m.Update(msg)
}

// findHistoryMatch returns the index of the newest inputHistory entry
// at or before startIdx whose content contains query as a substring,
// or -1 if no match exists. startIdx may be -1 or out of range, in
// which case the search starts from the newest entry.
func (m Model) findHistoryMatch(query string, startIdx int) int {
	if query == "" {
		return -1
	}
	if startIdx < 0 || startIdx >= len(m.inputHistory) {
		startIdx = len(m.inputHistory) - 1
	}
	for i := startIdx; i >= 0; i-- {
		if strings.Contains(m.inputHistory[i], query) {
			return i
		}
	}
	return -1
}

// refreshSearchMatch finds the newest history entry containing the
// current query and writes it into the textarea. On no match, the
// textarea is cleared (so the user sees they've over-typed past any
// real entry) and matchIdx stays -1 — the status bar's "(failing)"
// flag picks up on this.
func (m Model) refreshSearchMatch() Model {
	idx := m.findHistoryMatch(m.searchQuery, len(m.inputHistory)-1)
	m.searchMatchIdx = idx
	if idx >= 0 {
		return m.replaceInputWith(m.inputHistory[idx])
	}
	return m.replaceInputWith("")
}

// exitSearchMode clears search-overlay state. When commit is false, the
// pre-search textarea value is restored (cancel path). When commit is
// true, whatever the textarea currently holds (the match or empty)
// stays put — the user continues from there.
func (m Model) exitSearchMode(commit bool) Model {
	m.searchMode = false
	m.searchQuery = ""
	m.searchMatchIdx = -1
	if !commit {
		m = m.replaceInputWith(m.preSearchValue)
	}
	m.preSearchValue = ""
	// Reset history-nav state so a subsequent Up arrow walks from the
	// newest entry rather than from wherever the search left it.
	m.inputHistoryIdx = len(m.inputHistory)
	m.inputDraft = ""
	return m
}

// replaceInputWith resets the textarea to s and syncs its visible
// height. Used by input-history navigation to swap the in-progress
// composition for a recalled prior entry.
func (m Model) replaceInputWith(s string) Model {
	m.input.Reset()
	if s != "" {
		m.input.InsertString(s)
	}
	return m.syncInputHeight()
}

// clipboardResultMsg reports the outcome of a /copy invocation back
// into the Update loop. Carries the original error so the system turn
// can give the user a specific reason if the platform tool was missing
// or failed.
type clipboardResultMsg struct {
	err error
}

// copyClipboardCmd returns a tea.Cmd that writes text to the OS
// clipboard via the platform's native CLI (pbcopy on macOS, wl-copy /
// xclip / xsel on Linux, clip on Windows). The Cmd resolves to a
// clipboardResultMsg the Update loop turns into a system turn.
func copyClipboardCmd(ctx context.Context, text string) tea.Cmd {
	return func() tea.Msg {
		return clipboardResultMsg{err: copyToClipboard(ctx, text)}
	}
}

// publishTurn appends t to history AND emits the rendered line through
// the Printer so it appears in terminal scrollback. Returns the
// mutated Model and the Println command (callers tea.Batch it with any
// other commands they're returning).
//
// Guard: if styles is nil (zero-value Model, e.g., in external package
// tests that construct tui.Model{} directly), the Printer is also
// likely nil — skip rendering and return a no-op cmd. The history
// append still happens; only the visual emit is skipped.
func (m Model) publishTurn(t Turn) (Model, tea.Cmd) {
	m.history = append(m.history, t)
	if m.styles == nil || m.opts.Printer == nil {
		return m, func() tea.Msg { return nil }
	}
	rendered := renderTurn(t, m.contentWidth(), m.styles, m.mdRenderer)
	return m, m.opts.Printer.Println(rendered)
}

// lastTutorTurn returns the content of the most recent RoleTutor entry
// in history, or "" if no tutor has spoken yet. Used by /copy to pick
// the right turn to send to the clipboard.
func lastTutorTurn(history []Turn) string {
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role == RoleTutor {
			return history[i].Content
		}
	}
	return ""
}

// CapturePrinter is the exported alias for the test-only capturePrinter
// type so external test packages can inject a Printer that records
// emitted lines. Internal users should use capturePrinter directly.
type CapturePrinter = capturePrinter

// Lines returns the captured lines in emission order. Returned slice
// is a snapshot — mutating it does not affect future captures.
func (c *capturePrinter) Lines() []string {
	out := make([]string, len(c.lines))
	copy(out, c.lines)
	return out
}

// SetWidth is a test seam letting external test packages prime the
// Model with a terminal width without sending a WindowSizeMsg. Updates
// any width-derived state (textarea inner width, markdown renderer).
func (m *Model) SetWidth(w int) {
	m.width = w
	inputInnerWidth := m.contentWidth() - 2
	if inputInnerWidth < 8 {
		inputInnerWidth = 8
	}
	m.input.SetWidth(inputInnerWidth)
	m.mdRenderer = newMarkdownRenderer(m.contentWidth() - 8)
}

// recentTranscript renders up to the last maxTurns user/tutor turns as
// plain text for the explain-back gate. System/context turns are
// skipped (same exclusion buildBackendMessages applies). The caller
// passes history WITHOUT the just-published pending user turn (that
// turn is supplied separately as the gate's pendingTurn argument), so
// it is not duplicated in the gate input.
func recentTranscript(history []Turn, maxTurns int) string {
	var picked []Turn
	for i := len(history) - 1; i >= 0 && len(picked) < maxTurns; i-- {
		switch history[i].Role {
		case RoleUser, RoleTutor:
			picked = append([]Turn{history[i]}, picked...)
		}
	}
	var b strings.Builder
	for _, t := range picked {
		who := "Learner"
		if t.Role == RoleTutor {
			who = "Tutor"
		}
		fmt.Fprintf(&b, "%s: %s\n", who, t.Content)
	}
	return b.String()
}

// buildBackendMessages converts the local history into the backend's
// message format. RoleSystem turns are dropped (UI-only metadata);
// RoleContext turns ARE forwarded as backends.RoleSystem messages so
// the model sees runtime context like extraction results.
//
// If the resulting stack is non-empty and does not end with a user
// message — for example, after a Pass 2 AutoReply ContextMsg leaves
// a TRANSITION system message at the tail — a synthetic user turn is
// appended so backends that require "conversation must end with a
// user message" (Anthropic Claude via OpenRouter, etc.) accept the
// stack. The synthetic content explicitly directs the model to the
// trailing system context (e.g. a TRANSITION announcement) rather
// than letting it anchor on prior assistant turns.
func buildBackendMessages(history []Turn) []backends.Message {
	out := make([]backends.Message, 0, len(history)+1)
	for _, t := range history {
		switch t.Role {
		case RoleUser:
			out = append(out, backends.Message{Role: backends.RoleUser, Content: t.Content})
		case RoleTutor:
			out = append(out, backends.Message{Role: backends.RoleAssistant, Content: t.Content})
		case RoleContext:
			out = append(out, backends.Message{Role: backends.RoleSystem, Content: t.Content})
		case RoleSystem:
			// UI-only meta — not sent to backend.
		}
	}
	if len(out) > 0 && out[len(out)-1].Role != backends.RoleUser {
		out = append(out, backends.Message{Role: backends.RoleUser, Content: "Continue per the most recent system message above."})
	}
	return out
}
