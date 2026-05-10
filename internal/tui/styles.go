package tui

import "github.com/charmbracelet/lipgloss"

// Styles bundles the lipgloss styles the TUI applies. A single value is
// constructed once at startup (DefaultStyles) and shared across renderers
// so colors stay consistent. Fields are typed lipgloss.Style so callers
// can override individually for tests.
type Styles struct {
	UserLabel      lipgloss.Style
	TutorLabel     lipgloss.Style
	SystemLabel    lipgloss.Style
	FirewallMarker lipgloss.Style
	Error          lipgloss.Style
	InputPrompt    lipgloss.Style
	Header         lipgloss.Style
	Status         lipgloss.Style

	// InputBorderFocus is applied to the rounded border around the
	// textarea while it has focus — the agent-CLI signature where
	// the input area is visually demarcated and accented.
	InputBorderFocus lipgloss.Style

	// InputBorderBlur is the same border in a dimmer color when the
	// textarea is blurred (e.g., during a streaming response when the
	// outer Update suppresses input). Visual cue that input is
	// temporarily inert.
	InputBorderBlur lipgloss.Style

	// Spinner colors the in-status spinner glyph.
	Spinner lipgloss.Style
}

// DefaultStyles returns the production color scheme. Adaptive colors let
// the same scheme work on dark and light terminals; lipgloss picks the
// right side of each pair based on terminal background detection.
func DefaultStyles() *Styles {
	return &Styles{
		UserLabel: lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.AdaptiveColor{Light: "#1f6feb", Dark: "#58a6ff"}),
		TutorLabel: lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.AdaptiveColor{Light: "#1a7f37", Dark: "#3fb950"}),
		SystemLabel: lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.AdaptiveColor{Light: "#6e7781", Dark: "#8b949e"}),
		FirewallMarker: lipgloss.NewStyle().
			Italic(true).
			Foreground(lipgloss.AdaptiveColor{Light: "#6e7781", Dark: "#8b949e"}),
		Error: lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.AdaptiveColor{Light: "#cf222e", Dark: "#f85149"}),
		InputPrompt: lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.AdaptiveColor{Light: "#1f6feb", Dark: "#58a6ff"}),
		Header: lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.AdaptiveColor{Light: "#24292f", Dark: "#f0f6fc"}),
		Status: lipgloss.NewStyle().
			Faint(true).
			Foreground(lipgloss.AdaptiveColor{Light: "#6e7781", Dark: "#8b949e"}),
		InputBorderFocus: lipgloss.NewStyle().
			BorderForeground(lipgloss.AdaptiveColor{Light: "#1f6feb", Dark: "#58a6ff"}),
		InputBorderBlur: lipgloss.NewStyle().
			BorderForeground(lipgloss.AdaptiveColor{Light: "#8c959f", Dark: "#6e7681"}),
		Spinner: lipgloss.NewStyle().
			Foreground(lipgloss.AdaptiveColor{Light: "#1f6feb", Dark: "#58a6ff"}),
	}
}
