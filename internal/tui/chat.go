package tui

import (
	"strings"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
)

// Role classifies a turn in the chat history.
type Role int

const (
	RoleUser Role = iota
	RoleTutor
	// RoleSystem is a UI-only meta turn — intro messages, /help
	// output, /quit confirmation, slash-command feedback. NOT sent
	// to the backend in the conversation context (would bloat the
	// LLM input with help dumps and intro text). For system content
	// that DOES need to reach the model — e.g., extraction results
	// the mentor must reason over — use RoleContext.
	RoleSystem
	// RoleContext is a system-authored turn that IS sent to the
	// backend as backends.RoleSystem. Use for runtime context the
	// model must see: PDF/URL extraction results, async tool output,
	// etc. Renders with the same "system" label as RoleSystem so the
	// user sees a consistent UI; only the backend treatment differs.
	RoleContext
)

// Turn is one finalized (or in-progress) chat exchange entry. Pure data —
// no Bubble Tea types — so it can be constructed and rendered in tests.
type Turn struct {
	Role    Role
	Content string
}

// roleLabel returns the prefix shown in the gutter for r. The string has
// no styling applied; styles get layered on by renderTurn.
func roleLabel(r Role) string {
	switch r {
	case RoleUser:
		return "you"
	case RoleTutor:
		return "tutor"
	case RoleSystem, RoleContext:
		return "system"
	default:
		return "?"
	}
}

// styleForRole returns the label style for r from styles. styles must be
// non-nil; the caller (renderTurn / Model.View) constructs Styles once at
// startup and threads it through.
func styleForRole(r Role, styles *Styles) lipgloss.Style {
	switch r {
	case RoleUser:
		return styles.UserLabel
	case RoleTutor:
		return styles.TutorLabel
	case RoleSystem, RoleContext:
		return styles.SystemLabel
	default:
		return lipgloss.NewStyle()
	}
}

// renderTurn formats a single Turn for display. Output is multi-line
// when content contains newlines or wraps wider than width: every line
// gets the role label in the gutter so wrapped content is unambiguous.
//
// For RoleTutor with a non-nil mdRenderer the content is run through
// glamour first (bold/italic/lists/inline code/fenced code blocks).
// Glamour does its own word-wrapping at the renderer's configured
// width; renderTurn just applies the role-label gutter line-by-line
// to the styled output. For all other roles (and for tutor turns
// when mdRenderer is nil), the original wrap-and-prefix path runs.
//
// width is the available terminal width in cells. It's used to compute
// the body column (width - len(label) - 2 for ": "). If width is too
// small to wrap usefully (< labelWidth + 8), renderTurn falls back to
// no wrapping and lets the terminal truncate.
//
// renderTurn does not append a trailing newline so the caller can
// position the result freely.
func renderTurn(t Turn, width int, styles *Styles, mdRenderer *glamour.TermRenderer) string {
	label := roleLabel(t.Role)
	style := styleForRole(t.Role, styles)

	gutter := style.Render(label) + ": "
	plainGutter := label + ": "
	indent := strings.Repeat(" ", lipgloss.Width(plainGutter))

	// Tutor turns with markdown rendering: glamour handles wrapping;
	// we only need to apply the gutter to the first line and indent
	// subsequent lines.
	if t.Role == RoleTutor && mdRenderer != nil && t.Content != "" {
		rendered, err := mdRenderer.Render(t.Content)
		if err == nil {
			rendered = strings.Trim(rendered, "\n")
			lines := strings.Split(rendered, "\n")
			var out strings.Builder
			for i, line := range lines {
				if i > 0 {
					out.WriteByte('\n')
				}
				if i == 0 {
					out.WriteString(gutter)
				} else {
					out.WriteString(indent)
				}
				out.WriteString(line)
			}
			return out.String()
		}
		// Fall through to plain-text path on render error.
	}

	bodyWidth := width - lipgloss.Width(plainGutter)
	if bodyWidth < 8 {
		// Terminal too narrow for sane wrapping. Render unwrapped; the
		// terminal will visually truncate but no information is lost.
		return gutter + t.Content
	}

	// Process embedded newlines, then wrap each segment to bodyWidth.
	segments := strings.Split(t.Content, "\n")
	var out strings.Builder
	for i, seg := range segments {
		wrapped := wrapWords(seg, bodyWidth)
		wlines := strings.Split(wrapped, "\n")
		for j, wl := range wlines {
			if i == 0 && j == 0 {
				out.WriteString(gutter)
			} else {
				out.WriteString(indent)
			}
			out.WriteString(wl)
			if !(i == len(segments)-1 && j == len(wlines)-1) {
				out.WriteByte('\n')
			}
		}
	}
	return out.String()
}

// wrapWords breaks s into lines no wider than width terminal cells. It
// breaks on spaces only — long unbroken tokens (URLs, identifiers) are
// emitted on their own line and may still exceed width; that's the same
// trade-off browsers and pagers make.
//
// If width <= 0 or s fits in one line, s is returned unchanged.
func wrapWords(s string, width int) string {
	if width <= 0 || lipgloss.Width(s) <= width {
		return s
	}

	words := strings.Split(s, " ")
	var out strings.Builder
	var line strings.Builder
	for i, w := range words {
		if line.Len() == 0 {
			line.WriteString(w)
			continue
		}
		// +1 for the space we'd add before w
		if lipgloss.Width(line.String())+1+lipgloss.Width(w) <= width {
			line.WriteByte(' ')
			line.WriteString(w)
			continue
		}
		out.WriteString(line.String())
		out.WriteByte('\n')
		line.Reset()
		line.WriteString(w)
		_ = i
	}
	out.WriteString(line.String())
	return out.String()
}
