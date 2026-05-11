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
	// slashCopy copies the last tutor turn's content to the OS
	// clipboard via the platform's native CLI (pbcopy / xclip / etc).
	slashCopy
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
	case slashCopy:
		return "copy"
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
	case "copy", "y":
		return slashCopy, args
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
  /copy, /y         Copy the tutor's last reply to the system clipboard
`
	restBlock := `
Input:
  enter                 Send the message
  alt+enter             Insert a newline (most terminals)
  ctrl+j                Insert a newline (universal fallback)
  esc                   Cancel an in-flight tutor reply
  ctrl+u                Clear the input line
  up / down             Recall earlier / later input (at first / last line)
  ctrl+r                Reverse-search past inputs (enter to commit, esc to cancel)

Scrollback and selection:
  scroll up             Mouse wheel / trackpad scrolls the terminal's native scrollback
  click and drag        Select text natively; cmd+c (macOS) or ctrl+shift+c (Linux) to copy
  ctrl+l                Clear the visible screen (terminal-native; conversation stays in scrollback)

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
const hintText = "Available commands: /help · /copy · /quit"

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
