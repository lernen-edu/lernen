package tui

import (
	"fmt"
	"sort"
	"strings"
)

// slashCommand classifies the user's input. PRE_BUILD_ANSWERS step 12
// limits M1 to /help and /quit; everything else is either a normal
// message (slashNone) or an unknown slash (slashUnknown).
type slashCommand int

const (
	// slashNone means the input is not a slash command and should be
	// dispatched to the backend as a regular message.
	slashNone slashCommand = iota
	// slashHelp shows the available commands.
	slashHelp
	// slashQuit terminates the session.
	slashQuit
	// slashClear visually clears the conversation viewport but keeps
	// m.history intact so subsequent backend calls retain full context.
	// Recover the visual via /history.
	slashClear
	// slashHistory re-renders m.history into the viewport. Useful after
	// /clear or any time the user wants to scroll back to the start of
	// the conversation.
	slashHistory
	// slashCopy copies the last tutor turn's content to the OS
	// clipboard via the platform's native CLI (pbcopy / xclip / etc).
	slashCopy
	// slashSelect toggles program-level mouse capture. When off, the
	// terminal handles mouse drags natively so the user can click-drag
	// to select arbitrary text and copy with Cmd+C / Ctrl+Shift+C.
	// Trade-off while off: two-finger trackpad scroll arrives as
	// Up/Down arrows (cycles input history); use PgUp/PgDn instead.
	// Toggling /select again restores scroll-forwarding mouse capture.
	slashSelect
	// slashHint is a bare "/" — show a one-line list of available
	// commands so the user knows what they can type next.
	slashHint
	// slashUnknown is a leading-slash input that doesn't match any
	// recognized command.
	slashUnknown
)

// String returns the canonical command name (without the leading slash)
// for diagnostic logs and tests.
func (c slashCommand) String() string {
	switch c {
	case slashNone:
		return "none"
	case slashHelp:
		return "help"
	case slashQuit:
		return "quit"
	case slashClear:
		return "clear"
	case slashHistory:
		return "history"
	case slashCopy:
		return "copy"
	case slashSelect:
		return "select"
	case slashHint:
		return "hint"
	case slashUnknown:
		return "unknown"
	default:
		return "?"
	}
}

// parseSlashCommand inspects raw and reports what kind of input it is.
// Leading and trailing whitespace are tolerated. The first word after
// the slash is the command name (case-insensitive); anything after it
// is returned as args (already trimmed of leading whitespace) so future
// commands can take parameters without changing this signature.
//
// Inputs that don't begin with '/' return slashNone with raw unchanged.
func parseSlashCommand(raw string) (slashCommand, string) {
	trimmed := strings.TrimSpace(raw)
	if !strings.HasPrefix(trimmed, "/") {
		return slashNone, raw
	}
	// strip leading '/'
	rest := strings.TrimSpace(trimmed[1:])
	if rest == "" {
		// Bare "/" — surface the command-list hint instead of an error.
		return slashHint, ""
	}

	name, args, _ := strings.Cut(rest, " ")
	args = strings.TrimSpace(args)

	switch strings.ToLower(name) {
	case "help", "h", "?":
		return slashHelp, args
	case "quit", "exit", "q":
		return slashQuit, args
	case "clear", "cls":
		return slashClear, args
	case "history":
		return slashHistory, args
	case "copy", "y":
		return slashCopy, args
	case "select", "sel":
		return slashSelect, args
	default:
		return slashUnknown, args
	}
}

// helpText returns the body shown for /help. When extras is non-nil and
// non-empty, a "Forge:" section is inserted between the Commands block
// and the Input block, listing each extra command (sorted) with its
// description.
func helpText(extras map[string]string) string {
	commandsBlock := `Commands:
  /help, /h, /?     Show this help
  /quit, /exit, /q  Exit the session
  /clear, /cls      Clear the viewport (history is kept; /history restores)
  /history          Re-render the conversation history
  /copy, /y         Copy the tutor's last reply to the system clipboard
  /select, /sel     Toggle text-selection mode (disables mouse capture so
                    click-drag selects text; trade-off: scroll wheel will
                    cycle input history while on — use PgUp/PgDn instead)
`
	restBlock := `
Input:
  enter                 Send the message
  alt+enter             Insert a newline (most terminals)
  ctrl+j                Insert a newline (universal fallback)
  esc                   Cancel an in-flight tutor reply
  ctrl+u                Clear the input line
  ctrl+l                Clear the conversation viewport (history is kept)
  up / down             Recall earlier / later input (at first / last line)
  ctrl+r                Reverse-search past inputs (enter to commit, esc to cancel)

Scrollback (the conversation history above the input):
  fn + up / fn + down   Page up / page down on macOS
  pgup / pgdown         Page up / page down on Linux / external keyboard
  trackpad / wheel      Two-finger scroll on macOS, scroll wheel elsewhere

Selecting text (lernen captures the mouse for scrolling, so plain drag
won't select unless selection mode is on):
  /select               Toggle selection mode — drag freely without modifier
  hold option + drag    macOS Terminal.app and iTerm2 default (no toggle)
  hold shift + drag     Linux gnome-terminal, Windows Terminal (no toggle)
  cmd+c / ctrl+shift+c  Copy after selecting (terminal-native shortcut)

Note: Shift+Enter is not supported. The Bubble Tea v1 runtime does not
parse the Shift+Enter escape sequence; use alt+enter or ctrl+j instead.

Anything else you type goes to the tutor. Phase 1 firewall is active:
the tutor cannot reply with code blocks longer than 3 lines, and any
that slip through are replaced with a marker.`

	if len(extras) == 0 {
		return commandsBlock + restBlock
	}
	var sb strings.Builder
	sb.WriteString(commandsBlock)
	sb.WriteString("\nForge:\n")
	keys := make([]string, 0, len(extras))
	for k := range extras {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		desc := extras[k]
		if desc == "" {
			desc = "(no description)"
		}
		fmt.Fprintf(&sb, "  /%-16s%s\n", k, desc)
	}
	sb.WriteString(restBlock)
	return sb.String()
}

// hintText is the one-line command list shown when the user submits a
// bare "/". Lighter than /help so users discovering the surface aren't
// flooded with text — /help is the next stop if they want detail.
const hintText = "Available commands: /help · /clear · /history · /copy · /select · /quit"

// slashCommandName extracts the command name (lowercase, no leading
// slash) from raw input. Returns "" when raw is not a slash command
// or has no name. Used by handleSubmit to look up registered
// SlashHandlers entries when parseSlashCommand reports slashUnknown.
func slashCommandName(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if !strings.HasPrefix(trimmed, "/") {
		return ""
	}
	rest := strings.TrimSpace(trimmed[1:])
	if rest == "" {
		return ""
	}
	name, _, _ := strings.Cut(rest, " ")
	return strings.ToLower(name)
}
