package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// GateComponentDoneMsg is emitted by each component runner when it finishes.
type GateComponentDoneMsg struct {
	Component string // "build" | "comprehension" | "debug"
	Outcome   string // "pass" | "fail" | "infra_error"
	Detail    string
}

// GateVerdictMsg is emitted by OnFinalize when it has computed the overall
// pass/fail verdict for the gate attempt.
type GateVerdictMsg struct {
	OverallPass bool
	Lines       []string
}

// GateOptions configures a GateModel. All On* callbacks are injected by the
// CLI (Task 10); tests inject stubs so the model is pure (no real I/O).
type GateOptions struct {
	PreconditionMet  bool
	PreconditionText string
	OnBuild          func() tea.Cmd
	OnComprehension  func() tea.Cmd
	OnDebug          func() tea.Cmd
	// OnFinalize receives the per-component outcomes and emits GateVerdictMsg.
	OnFinalize func(outcomes map[string]string) tea.Cmd
	// Printer follows the tui package convention (Printer interface + teaPrinter
	// default defined in app.go). Optional: GateModel currently emits nothing
	// via Printer at runtime — the harness/Program drives display via View().
	Printer Printer
}

// GateModel drives the per-component gate flow:
//
//	build → comprehension → debug → OnFinalize → GateVerdictMsg
//
// It is a pure Bubble Tea model: all side-effecting work is dispatched through
// the injected On* callbacks; no real I/O is performed here.
//
// infra_error is non-terminal/resumable (spec §2.4): on an infra_error outcome
// the model suspends (suspended=true) without setting done, and the CLI persists
// the sidecar as-is so the learner can fix the environment and re-run.
type GateModel struct {
	opts     GateOptions
	order    []string
	idx      int
	outcomes map[string]string
	// suspended is true when an infra_error has paused the session.
	// Resumable: done remains false; the CLI persists the sidecar as-is.
	suspended bool
	done      bool
	verdict   GateVerdictMsg
	queued    tea.Cmd
}

// NewGate constructs a GateModel from opts.
func NewGate(opts GateOptions) GateModel {
	if opts.Printer == nil {
		opts.Printer = teaPrinter{}
	}
	return GateModel{
		opts:     opts,
		order:    []string{"build", "comprehension", "debug"},
		outcomes: map[string]string{},
	}
}

// lastCmd executes the currently queued cmd and returns its message.
// Used by tests via the drain helper — never called in production.
func (m GateModel) lastCmd() tea.Msg {
	if m.queued == nil {
		return nil
	}
	return m.queued()
}

// runner returns the On* callback for the given component name.
func (m GateModel) runner(component string) func() tea.Cmd {
	switch component {
	case "build":
		return m.opts.OnBuild
	case "comprehension":
		return m.opts.OnComprehension
	default:
		return m.opts.OnDebug
	}
}

// Init kicks off the first component (build).
func (m GateModel) Init() tea.Cmd {
	return m.runner(m.order[0])()
}

// Update handles GateComponentDoneMsg and GateVerdictMsg.
//
// infra_error: sets suspended=true and quits without setting done (resumable).
// Non-infra outcomes: advances to the next component, or to OnFinalize when
// all three components are complete.
// GateVerdictMsg: sets done=true and verdict, then quits.
func (m GateModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch t := msg.(type) {
	case GateComponentDoneMsg:
		if t.Outcome == "infra_error" {
			// Non-terminal, resumable: suspend without finalising.
			// CLI persists sidecar as-is (spec §2.4).
			m.suspended = true
			return m, tea.Quit
		}
		m.outcomes[t.Component] = t.Outcome
		m.idx++
		if m.idx < len(m.order) {
			next := m.runner(m.order[m.idx])()
			m.queued = next
			return m, next
		}
		// All components done — finalize.
		fin := m.opts.OnFinalize(m.outcomes)
		m.queued = fin
		return m, fin
	case GateVerdictMsg:
		m.done = true
		m.verdict = t
		return m, tea.Quit
	case tea.KeyMsg:
		if t.String() == "ctrl+c" {
			return m, tea.Quit
		}
	}
	return m, nil
}

// View renders current gate progress. In production the Bubble Tea Program
// calls this; tests do not exercise View directly.
func (m GateModel) View() string {
	if m.done {
		v := "FAIL"
		if m.verdict.OverallPass {
			v = "PASS"
		}
		return fmt.Sprintf("Gate verdict: %s\n%s\n", v, strings.Join(m.verdict.Lines, "\n"))
	}
	if m.suspended {
		return "Gate paused (environment issue). Fix it and re-run `lernen gate` to resume.\n"
	}
	return fmt.Sprintf("%s\nComponent %d/%d…\n", m.opts.PreconditionText, m.idx+1, len(m.order))
}
