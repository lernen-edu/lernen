package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// PracticeOptions configures a no-backend practice session. There is
// deliberately no Backend / ExplainBackGate field — practice never
// dispatches to a model (PRD §4.6 "no AI tutor").
type PracticeOptions struct {
	Prompt        string
	Workdir       string
	Printer       Printer
	REPLAvailable bool
	REPLCmd       string
	// OnSubmit grades the current workdir and returns a Cmd resolving
	// to PracticeDoneMsg (terminal — recorded outcome) or
	// PracticeInfraErrorMsg (non-terminal — nothing recorded, session
	// stays open for retry). Injected by cli/practice.go.
	OnSubmit func() tea.Cmd
	// OnDocs runs a DocsProvider query for the given library/topic and
	// returns a Cmd resolving to a SystemMsg-style note. nil ⇒ /docs
	// reports docs are unavailable.
	OnDocs func(arg string) tea.Cmd
}

// PracticeDoneMsg ends the session: the grader recorded (or declined
// to record) and the program should print the summary and quit.
type PracticeDoneMsg struct {
	Outcome string // "" when nothing recorded (infra error)
	Summary string
}

// PracticeInfraErrorMsg signals an in-session test-runner or toolchain
// failure (pytest absent, exec failure, context timeout, zero tests
// collected). Nothing is recorded and the session stays open so the
// learner can fix their environment and /submit again.
type PracticeInfraErrorMsg struct {
	Detail string
}

type practiceSubmitMsg struct{}

// PracticeModel is a minimal Bubble Tea model: it shows the prompt +
// workdir, accepts a small slash set, and delegates grading via
// OnSubmit. It holds no backend.
type PracticeModel struct {
	opts  PracticeOptions
	input string
	done  bool
}

// NewPractice constructs the model.
func NewPractice(opts PracticeOptions) PracticeModel {
	if opts.Printer == nil {
		opts.Printer = teaPrinter{}
	}
	return PracticeModel{opts: opts}
}

func (m PracticeModel) Init() tea.Cmd { return nil }

func (m PracticeModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case practiceSubmitMsg:
		if m.opts.OnSubmit != nil {
			return m, m.opts.OnSubmit()
		}
		return m, nil
	case PracticeInfraErrorMsg:
		// Non-terminal: nothing recorded; session stays open for retry.
		line := fmt.Sprintf("Test runner error: %s. Fix the environment and /submit again.", msg.Detail)
		c := m.opts.Printer.Println(line)
		return m, c
	case PracticeDoneMsg:
		m.done = true
		line := "Practice session ended (nothing recorded)."
		if msg.Outcome != "" {
			line = fmt.Sprintf("Recorded: %s (%s).", msg.Outcome, msg.Summary)
		}
		c := m.opts.Printer.Println(line)
		return m, tea.Sequence(c, tea.Quit)
	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m PracticeModel) handleKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.Type {
	case tea.KeyEnter:
		text := strings.TrimSpace(m.input)
		m.input = ""
		return m.dispatch(text)
	case tea.KeyRunes, tea.KeySpace:
		m.input += string(k.Runes)
		return m, nil
	case tea.KeyBackspace:
		if m.input != "" {
			m.input = m.input[:len(m.input)-1]
		}
		return m, nil
	case tea.KeyCtrlC:
		return m, tea.Quit
	}
	return m, nil
}

func (m PracticeModel) dispatch(text string) (tea.Model, tea.Cmd) {
	switch {
	case text == "/quit":
		return m, tea.Quit
	case text == "/submit":
		return m, func() tea.Msg { return practiceSubmitMsg{} }
	case text == "/help":
		c := m.opts.Printer.Println("/submit run the tests · /repl open a REPL · /docs <lib> · /quit")
		return m, c
	case text == "/repl":
		if !m.opts.REPLAvailable {
			c := m.opts.Printer.Println("REPL unavailable (python3 not on PATH).")
			return m, c
		}
		c := m.opts.Printer.Println(fmt.Sprintf("Run `%s` in %s in another terminal.", m.opts.REPLCmd, m.opts.Workdir))
		return m, c
	case text == "/docs" || strings.HasPrefix(text, "/docs "):
		if m.opts.OnDocs == nil {
			c := m.opts.Printer.Println("docs unavailable in this session.")
			return m, c
		}
		return m, m.opts.OnDocs(strings.TrimSpace(strings.TrimPrefix(text, "/docs")))
	}
	c := m.opts.Printer.Println("Practice is AI-off. Use /submit, /repl, /docs, or /quit.")
	return m, c
}

func (m PracticeModel) View() string {
	if m.done {
		return ""
	}
	return fmt.Sprintf("Practice — workdir: %s\n\n%s\n\n› %s",
		m.opts.Workdir, m.opts.Prompt, m.input)
}
