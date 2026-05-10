// Package tui implements the Bubble Tea-driven chat interface (PRD §4.1)
// that hosts a Phase 1 tutor session. The TUI's load-bearing job is to
// honor the no-flicker invariant from PRD §4.1 and PRE_BUILD_ANSWERS §3:
// the body of an open code block — fenced or indented — must never reach
// the screen until the firewall has confirmed it is at most three lines.
//
// Implementation: a phase1.Streamer mediates between the Backend's token
// channel and the rendered tutor turn. Tokens flow Write → safe-suffix →
// pending.content; the Streamer holds code-block bodies until the
// boundary is known. View() composes the chat history + in-flight pending
// turn into a viewport, then renders header / viewport / textinput / status
// as four pinned regions.
package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
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
// Contract: handlers own history and viewport. Specifically, the
// dispatch path does NOT append a roleUser turn for the input that
// triggered the handler, and it does NOT auto-refresh the viewport
// after the handler returns. If the handler wants the typed command
// to appear in the transcript, it must append a roleUser turn itself;
// if it wants its appended turns to render before the next event, it
// must call m.refreshViewportToBottom() (or equivalent) before
// returning. This freedom lets, e.g., /wrap suppress the echo and
// render the structuring result directly.
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
	// or the structured YAML. The intro persists in m.history across
	// /clear (viewport-only) and reappears on /history.
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
	// set (/help, /quit, /clear, /history, /copy). Built-ins always
	// take precedence; SlashHandlers fires only for commands that
	// parseSlashCommand returns slashUnknown for. The forge registers
	// /wrap here. Map keys are lowercase command names (no leading
	// slash).
	SlashHandlers map[string]SlashHandler

	// SlashHandlerHelp provides one-line descriptions for the
	// SlashHandlers entries, surfaced in /help. Map keys must match
	// SlashHandlers keys; missing entries default to "(no description)".
	SlashHandlerHelp map[string]string

	// DispatchCtx is the context handed to backend.StreamChat. Defaults
	// to context.Background when zero.
	DispatchCtx context.Context
}

// Model is the Bubble Tea model holding all session state.
type Model struct {
	opts   Options
	styles *Styles

	input    textarea.Model
	viewport viewport.Model
	spinner  spinner.Model
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
	height  int
	waiting bool

	violations []phase1.Violation

	// cancelStream is the per-stream cancel function set in handleSubmit
	// and cleared in streamDoneMsg / streamErrMsg. Pressing Esc while
	// waiting calls this; nil when no stream is in flight.
	cancelStream context.CancelFunc

	// quitting is set when /quit (or Ctrl+C) is received. View renders
	// nothing in this state so the terminal cleans up cleanly.
	quitting bool

	// mouseEnabled tracks whether program-level mouse capture is on.
	// Defaults to true (matches the WithMouseCellMotion option used by
	// production runners — see cli/work.go and cli/forge.go). The user
	// can flip it via /select: when off, scroll wheel events fall
	// through to the terminal so click-drag text selection works
	// natively (at the cost of two-finger trackpad scroll being
	// interpreted as Up/Down arrows by the terminal). Toggle mid-
	// session via tea.EnableMouseCellMotion / tea.DisableMouse.
	mouseEnabled bool

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
// history. Used by external callers that need to inject status messages
// without touching internal fields. Returns the mutated Model so it
// can be threaded through Bubble Tea's Model→(Model, Cmd) flow.
func (m Model) AppendSystemTurn(text string) Model {
	m.history = append(m.history, Turn{Role: RoleSystem, Content: text})
	m = m.refreshViewportToBottom()
	return m
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
type ContextMsg struct{ Text string }

// QuitWithMessage is a tea.Msg that appends Text as a system turn,
// then quits the program. Note: in alt-screen TUIs (the production
// configuration), the appended turn is not rendered before the
// alt-screen is torn down — callers needing a user-visible farewell
// in altscreen mode should print to stdout AFTER the program exits.
type QuitWithMessage struct{ Text string }

// New constructs a fresh Model. The textarea is focused immediately so
// the user can start typing the first message.
func New(opts Options) Model {
	if opts.DispatchCtx == nil {
		opts.DispatchCtx = context.Background()
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

	vp := viewport.New(80, 20) // dimensions updated on first WindowSizeMsg
	// Restrict viewport keybindings: only PgUp/PgDn navigate the scrollback,
	// so single-character keys (j/k/h/l/f/b/u/d/space) reach the textarea.
	// Mouse-wheel and trackpad two-finger scroll are forwarded via the
	// program-level tea.WithMouseCellMotion in cli/work.go's runner;
	// MouseWheelEnabled is set to true by viewport.New, so wheel events
	// in the fall-through path scroll the viewport. Selection via plain
	// click-drag is blocked by the mouse capture; users hold Option
	// (macOS) or Shift (Linux/Windows) to bypass — documented in helpText.
	vp.KeyMap = viewport.KeyMap{
		PageDown: key.NewBinding(key.WithKeys("pgdown")),
		PageUp:   key.NewBinding(key.WithKeys("pgup")),
	}

	sp := spinner.New(
		spinner.WithSpinner(spinner.Dot),
		spinner.WithStyle(styles.Spinner),
	)

	newStreamer := phase1.NewStreamer
	if opts.DisableFirewall {
		newStreamer = phase1.NewPassthroughStreamer
	}
	m := Model{
		opts:         opts,
		styles:       styles,
		input:        ta,
		viewport:     vp,
		spinner:      sp,
		streamer:     newStreamer(),
		newStreamer:  newStreamer,
		mouseEnabled: true,
	}
	if intro := strings.TrimSpace(opts.IntroMessage); intro != "" {
		m.history = append(m.history, Turn{Role: RoleSystem, Content: intro})
	}
	return m
}

// Init starts the textarea's cursor blink ticker and the spinner's
// animation tick. The spinner self-perpetuates via spinner.TickMsg in
// Update; we only render its frame when the model is waiting on a
// stream, so the cost of letting it tick continuously is negligible.
func (m Model) Init() tea.Cmd {
	return tea.Batch(textarea.Blink, m.spinner.Tick)
}

// Update is the message-dispatch heart of the TUI.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		// Input width is the inner content width of the bordered box —
		// outer width (m.contentWidth) minus the 2 cells the rounded
		// border consumes (left + right).
		inputInnerWidth := m.contentWidth() - 2
		if inputInnerWidth < 8 {
			inputInnerWidth = 8
		}
		m.input.SetWidth(inputInnerWidth)
		m.mdRenderer = newMarkdownRenderer(m.contentWidth() - 8)
		m = m.applyLayout()
		m = m.refreshViewport()
		return m, nil

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
		case "ctrl+l":
			// Clear the viewport, same as /clear. Doesn't touch m.history,
			// so the next backend turn keeps full context.
			if m.waiting {
				return m, nil
			}
			m.viewport.SetContent("")
			m.viewport.GotoTop()
			return m, nil
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
		m.history = append(m.history, Turn{Role: RoleSystem, Content: line})
		m = m.refreshViewportToBottom()
		return m, nil

	case streamReadyMsg:
		m.tokenCh = msg.ch
		return m, waitForToken(msg.ch)

	case tokenMsg:
		wasAtBottom := m.viewport.AtBottom()
		if m.pending != nil {
			out, vs := m.streamer.Write([]byte(msg.text))
			m.pending.Content += out
			m.violations = append(m.violations, vs...)
		}
		m = m.refreshViewportConditional(wasAtBottom)
		if m.tokenCh == nil {
			// Defensive: tokenMsg arrived after stream cleared. Shouldn't
			// happen in normal flow but stop pulling rather than panic.
			return m, nil
		}
		return m, waitForToken(m.tokenCh)

	case streamDoneMsg:
		wasAtBottom := m.viewport.AtBottom()
		out, vs := m.streamer.Flush()
		if m.pending != nil {
			m.pending.Content += out
			m.history = append(m.history, *m.pending)
			m.pending = nil
		}
		m.violations = append(m.violations, vs...)
		m.streamer = m.newStreamer()
		m.tokenCh = nil
		m.waiting = false
		m = m.clearCancel()
		m = m.refreshViewportConditional(wasAtBottom)
		return m, nil

	case streamErrMsg:
		wasAtBottom := m.viewport.AtBottom()
		// User-cancellation path: the partial pending turn — if it has
		// any content — is committed to history with a [cancelled]
		// marker so the user can see what they interrupted. An empty
		// pending turn is dropped entirely so the history stays clean.
		// No "backend error: ..." surface for cancellations.
		if errors.Is(msg.err, context.Canceled) {
			out, vs := m.streamer.Flush()
			if m.pending != nil {
				m.pending.Content += out
				if strings.TrimSpace(m.pending.Content) != "" {
					m.pending.Content = strings.TrimRight(m.pending.Content, "\n") + "\n\n" + cancelledMarker
					m.history = append(m.history, *m.pending)
				}
				m.pending = nil
			}
			m.violations = append(m.violations, vs...)
			m.streamer = m.newStreamer()
			m.tokenCh = nil
			m.waiting = false
			m = m.clearCancel()
			m = m.refreshViewportConditional(wasAtBottom)
			return m, nil
		}

		// Real error path: drop partial content and surface a system turn.
		m.pending = nil
		m.streamer = m.newStreamer()
		m.tokenCh = nil
		m.waiting = false
		m = m.clearCancel()
		m.history = append(m.history, Turn{
			Role:    RoleSystem,
			Content: fmt.Sprintf("backend error: %s", msg.err),
		})
		m = m.refreshViewportConditional(wasAtBottom)
		return m, nil

	case SystemMsg:
		m.history = append(m.history, Turn{Role: RoleSystem, Content: msg.Text})
		m = m.refreshViewportToBottom()
		return m, nil

	case ContextMsg:
		m.history = append(m.history, Turn{Role: RoleContext, Content: msg.Text})
		m = m.refreshViewportToBottom()
		return m, nil

	case QuitWithMessage:
		m.history = append(m.history, Turn{Role: RoleSystem, Content: msg.Text})
		m = m.refreshViewportToBottom()
		m.quitting = true
		return m, tea.Quit
	}

	// Forward unhandled messages. Viewport sees them so PgUp / PgDn and
	// mouse-wheel / trackpad scroll work even mid-stream; textarea sees
	// them only when not waiting (so background tokens don't interfere
	// with input).
	var vpCmd, inCmd tea.Cmd
	m.viewport, vpCmd = m.viewport.Update(msg)
	if !m.waiting {
		m.input, inCmd = m.input.Update(msg)
		// The textarea's content may have just gained or lost a newline;
		// resize it (and the viewport, which loses rows when input grows)
		// so the four-region layout stays balanced.
		m = m.syncInputHeight()
	}
	return m, tea.Batch(vpCmd, inCmd)
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
		m.history = append(m.history, Turn{Role: RoleUser, Content: text})
		m.history = append(m.history, Turn{Role: RoleSystem, Content: helpText(m.opts.SlashHandlerHelp)})
		m = m.refreshViewportToBottom()
		return m, nil
	case slashClear:
		// Visual-only clear: keep m.history (so the next backend call
		// retains full context) but rewrite the viewport with no turns.
		// /history rehydrates the visual.
		m.viewport.SetContent("")
		m.viewport.GotoTop()
		return m, nil
	case slashHistory:
		// Re-render m.history into the viewport. After /clear the viewport
		// content is empty; this command brings it back. Also useful when
		// the user just wants to re-examine the entire conversation.
		m = m.refreshViewportToBottom()
		return m, nil
	case slashCopy:
		// Copy the most recent tutor turn's content to the OS clipboard.
		// User input is the user's own text, copyable via terminal-native
		// selection (Option-drag on macOS), so /copy targets the tutor side.
		last := lastTutorTurn(m.history)
		m.history = append(m.history, Turn{Role: RoleUser, Content: text})
		if last == "" {
			m.history = append(m.history, Turn{
				Role:    RoleSystem,
				Content: "no tutor reply yet — nothing to copy.",
			})
			m = m.refreshViewportToBottom()
			return m, nil
		}
		m = m.refreshViewportToBottom()
		return m, copyClipboardCmd(m.opts.DispatchCtx, last)
	case slashSelect:
		// Toggle program-level mouse capture so click-drag text
		// selection works without holding Option/Shift. Use Bubble
		// Tea's runtime mouse commands (DisableMouse / EnableMouse-
		// CellMotion) so the change takes effect immediately.
		m.history = append(m.history, Turn{Role: RoleUser, Content: text})
		var (
			toggleCmd tea.Cmd
			msg       string
		)
		if m.mouseEnabled {
			m.mouseEnabled = false
			toggleCmd = tea.DisableMouse
			msg = "Selection mode ON — click-drag to select text, Cmd+C / Ctrl+Shift+C to copy.\n" +
				"Trade-off: trackpad and scroll-wheel won't scroll the conversation while selection is on (most terminals translate them into Up/Down arrows, which cycle input history). Use PgUp / PgDn / fn+Up / fn+Down to scroll instead.\n" +
				"Type /select again to restore scroll-forwarding."
		} else {
			m.mouseEnabled = true
			toggleCmd = tea.EnableMouseCellMotion
			msg = "Selection mode OFF — trackpad / scroll-wheel scroll the conversation again. Hold Option (macOS) or Shift (Linux/Windows) while dragging to select text without toggling."
		}
		m.history = append(m.history, Turn{Role: RoleSystem, Content: msg})
		m = m.refreshViewportToBottom()
		return m, toggleCmd
	case slashHint:
		m.history = append(m.history, Turn{Role: RoleUser, Content: text})
		m.history = append(m.history, Turn{Role: RoleSystem, Content: hintText})
		m = m.refreshViewportToBottom()
		return m, nil
	case slashUnknown:
		if name := slashCommandName(text); name != "" {
			if h, ok := m.opts.SlashHandlers[name]; ok {
				return h(m, args)
			}
		}
		// existing unknown-command body unchanged
		m.history = append(m.history, Turn{Role: RoleUser, Content: text})
		m.history = append(m.history, Turn{
			Role:    RoleSystem,
			Content: fmt.Sprintf("unknown command: %q. Try /help.", text),
		})
		m = m.refreshViewportToBottom()
		return m, nil
	}

	// slashNone: dispatch to backend.
	m.history = append(m.history, Turn{Role: RoleUser, Content: text})
	m.pending = &Turn{Role: RoleTutor, Content: ""}
	m.waiting = true
	m.streamer = m.newStreamer() // fresh state machine per turn

	streamCtx, cancel := context.WithCancel(m.opts.DispatchCtx)
	m.cancelStream = cancel

	msgs := buildBackendMessages(m.history)
	m = m.refreshViewportToBottom()
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

// applyLayout pushes current dimensions into the viewport. Called from
// WindowSizeMsg and from syncInputHeight; centralizing the math here keeps
// View() pure and prevents scattered `m.height - N` arithmetic across
// renderers.
func (m Model) applyLayout() Model {
	_, vph, _, _ := m.layoutHeights()
	m.viewport.Width = m.contentWidth()
	m.viewport.Height = vph
	return m
}

// layoutHeights returns the heights of each pinned region. The viewport
// claims whatever vertical space is left over after the header, a blank
// padding row, the bordered input region, and the status line. Output
// rows breakdown:
//
//	header (0 or 2) + viewport + 1 (padding row) + input_box + status (1) = m.height
//
// where input_box = m.input.Height() + 2 (top + bottom border cells of
// the rounded border around the textarea). A 3-row floor avoids
// degenerate viewports on extremely short terminals.
func (m Model) layoutHeights() (header, viewport, input, status int) {
	header = m.headerHeight()
	status = 1
	input = m.input.Height() + 2 // +2 for top/bottom border rows
	const padding = 1
	viewport = m.height - header - padding - input - status
	if viewport < 3 {
		viewport = 3
	}
	return
}

// syncInputHeight resizes the textarea to fit current content (clamped
// by maxInputHeight) and reapplies the layout so the viewport reclaims
// any rows the input gave back, or surrenders rows the input now needs.
// Called after every state change that could shift line count: the
// fall-through KeyMsg path, Reset on submit, InsertString from
// Shift+Enter / Ctrl+J / Alt+Enter.
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
	return m.applyLayout()
}

// headerHeight reports how many vertical lines the header occupies.
// Returns 0 when renderHeader produces no output; otherwise 2 (header
// line + blank separator) to match the rendering in View().
func (m Model) headerHeight() int {
	if m.renderHeader() == "" {
		return 0
	}
	return 2
}

// newMarkdownRenderer constructs a glamour TermRenderer with the dark
// stylesheet and the supplied word-wrap width. Returns nil on init
// error — callers (renderTurn, renderConversation) treat nil as
// "render plain text" so a renderer-init failure can never crash the
// TUI mid-session.
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

// refreshViewport rewrites the viewport content from current history+pending
// without changing scroll position.
func (m Model) refreshViewport() Model {
	m.viewport.SetContent(renderConversation(m.history, m.pending, m.contentWidth(), m.styles, m.mdRenderer))
	return m
}

// refreshViewportToBottom rewrites the viewport content and unconditionally
// scrolls to the bottom — used when the user submits a new turn (their own
// message should always come into view).
//
// Guard: if styles is nil (zero-value Model, e.g., in external package tests
// that call AppendSystemTurn on a tui.Model{} fixture), skip the viewport
// re-render. The history append still happens; only the visual refresh is
// skipped.
func (m Model) refreshViewportToBottom() Model {
	if m.styles == nil {
		return m
	}
	m.viewport.SetContent(renderConversation(m.history, m.pending, m.contentWidth(), m.styles, m.mdRenderer))
	m.viewport.GotoBottom()
	return m
}

// refreshViewportConditional rewrites the viewport content and only scrolls
// to the bottom if the user was already there. Streaming chunks shouldn't
// yank a user reading older context; on the other hand a user already at
// the bottom expects new tokens to stay in view.
func (m Model) refreshViewportConditional(wasAtBottom bool) Model {
	m.viewport.SetContent(renderConversation(m.history, m.pending, m.contentWidth(), m.styles, m.mdRenderer))
	if wasAtBottom {
		m.viewport.GotoBottom()
	}
	return m
}

// View renders the current Model state into a single string for the
// terminal. Layout: header → viewport → blank padding row → bordered
// input → status. The four-region agent-CLI layout (Claude Code,
// Codex, Gemini-CLI, OpenCode, Pi) places visual emphasis on the
// bordered input area; that border is the most iconic signature of
// the genre and the focus-accent color is what telegraphs "I'm
// listening for input" vs. "I'm processing."
func (m Model) View() string {
	if m.quitting {
		return ""
	}
	if m.width == 0 {
		// First-paint guard: no WindowSizeMsg yet, so the viewport's own
		// dimensions are stale. Rendering would produce a misshapen frame.
		return ""
	}

	var sb strings.Builder
	if header := m.renderHeader(); header != "" {
		sb.WriteString(header)
		sb.WriteString("\n\n")
	}
	sb.WriteString(m.viewport.View())
	sb.WriteString("\n\n") // one blank padding row between viewport and input
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

// renderHeader produces the sticky header line. If Options.HeaderText
// is set, that string is rendered as the header (forge use). Otherwise
// the curriculum+chapter banner renders if both are set; the empty
// string is returned when neither header source is available.
func (m Model) renderHeader() string {
	if text := strings.TrimSpace(m.opts.HeaderText); text != "" {
		return m.styles.Header.Render(text)
	}
	if m.opts.Curriculum == nil {
		return ""
	}
	parts := []string{m.opts.Curriculum.Metadata.Name}
	if m.opts.Chapter != nil {
		parts = append(parts, m.opts.Chapter.Title)
	}
	return m.styles.Header.Render(strings.Join(parts, " — "))
}

// renderStatus produces the agent-CLI status line shown beneath the
// bordered input. While waiting: spinner + "thinking" + model + cancel
// hint. While idle: model + canonical key bindings. While searching
// (Ctrl+R): bash-readline-style "(reverse-i-search)`query':" with a
// "(failing)" flag when no match. Segments are joined with " · "
// separators and dimmed via styles.Status. The model segment is
// omitted when ModelLabel is empty (tests, fixtures).
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
		// Render the spinner glyph un-dimmed so the animation reads
		// clearly against the rest of the (faint) status line.
		segments = append(segments, m.spinner.View()+" thinking")
	}

	if m.opts.ModelLabel != "" {
		segments = append(segments, m.opts.ModelLabel)
	}

	if m.waiting {
		segments = append(segments, "esc to cancel")
	} else {
		segments = append(segments, "enter send · alt+enter newline · /help · ctrl+c quit")
	}

	return " " + m.styles.Status.Render(strings.Join(segments, " · "))
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

// replaceInputWith resets the textarea to s, syncs its visible height,
// and reapplies the layout so the viewport reclaims (or surrenders)
// rows. Used by input-history navigation to swap the in-progress
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

// buildBackendMessages converts the local history into the backend's
// message format. RoleSystem turns are dropped (UI-only metadata);
// RoleContext turns ARE forwarded as backends.RoleSystem messages so
// the model sees runtime context like extraction results.
func buildBackendMessages(history []Turn) []backends.Message {
	out := make([]backends.Message, 0, len(history))
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
	return out
}
