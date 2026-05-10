package tui

import (
	"context"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lernen-edu/lernen/internal/backends"
)

// Message types
//
// These are the tea.Msg values the TUI's Update method handles. Bubble Tea
// dispatches them serially, so each handler can mutate the Model freely
// without locking.

// submitMsg signals that the user has finalized an input line and pressed
// Enter. The Model decides whether to dispatch it to the backend or to
// handle it locally as a slash command.
type submitMsg struct {
	text string
}

// NewSubmitMsg returns a tea.Msg that triggers the same Model path as
// pressing Enter with text typed in the input box. Exported for use by
// integration tests in external packages (e.g. package goals_test) that
// need to drive the session headlessly without importing Bubble Tea
// internals or reaching for unexported types.
func NewSubmitMsg(text string) tea.Msg {
	return submitMsg{text: text}
}

// streamReadyMsg arrives after a successful backend.StreamChat call. The
// channel emits backends.Token values; the Model captures it and starts
// pulling tokens via waitForToken.
type streamReadyMsg struct {
	ch <-chan backends.Token
}

// tokenMsg carries one Token.Text from the backend stream.
type tokenMsg struct {
	text string
}

// streamErrMsg signals a terminal stream error — either a synchronous
// failure from backends.StreamChat (e.g., HTTP error before the channel
// opens) or a Token.Err from the channel itself.
type streamErrMsg struct {
	err error
}

// streamDoneMsg signals that the backend closed the channel cleanly.
type streamDoneMsg struct{}

// dispatchStream returns a tea.Cmd that calls backend.StreamChat and
// reports the result. If StreamChat returns an error synchronously, the
// Cmd resolves to streamErrMsg. Otherwise the Cmd resolves to
// streamReadyMsg carrying the live channel; the Model is then expected
// to call waitForToken(ch) to pull the first token.
func dispatchStream(ctx context.Context, b backends.Backend, history []backends.Message, systemPrompt string) tea.Cmd {
	return func() tea.Msg {
		ch, err := b.StreamChat(ctx, history, systemPrompt)
		if err != nil {
			return streamErrMsg{err: err}
		}
		return streamReadyMsg{ch: ch}
	}
}

// waitForToken returns a tea.Cmd that reads exactly one Token from ch
// and resolves it into the appropriate message. After receiving a
// tokenMsg, the Model returns waitForToken(ch) again to read the next
// token, until streamDoneMsg or streamErrMsg arrives.
func waitForToken(ch <-chan backends.Token) tea.Cmd {
	return func() tea.Msg {
		tok, ok := <-ch
		if !ok {
			return streamDoneMsg{}
		}
		if tok.Err != nil {
			return streamErrMsg{err: tok.Err}
		}
		return tokenMsg{text: tok.Text}
	}
}
