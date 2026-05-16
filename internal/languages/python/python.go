// Package python implements the LanguageAdapter for Python in v0.
//
// The M1 stub provides only what `lernen work` needs: ID, DisplayName,
// ToolchainCheck (probes for python3 on PATH), and SystemPromptAddendum
// (the locked guidance text from docs/PRE_BUILD_ANSWERS.md §11). The full
// implementation — pytest with pytest-json-report, ruff for format/lint,
// the complete competency taxonomy, common error patterns — lands in M3
// per BUILD_ORDER.md.
package python

import (
	_ "embed"
	"context"
	"os/exec"
	"strings"

	"github.com/lernen-edu/lernen/internal/languages"
)

// addendumRaw is the literal Python-specific tutor guidance, embedded from
// python_addendum.md. The text is locked in docs/PRE_BUILD_ANSWERS.md §11.
//
//go:embed python_addendum.md
var addendumRaw string

// addendum is the trimmed addendum exposed to the harness. Trimming the
// trailing newline keeps the appended-after-main-prompt formatting clean.
var addendum = strings.TrimRight(addendumRaw, "\n")

// Adapter implements languages.LanguageAdapter for Python.
type Adapter struct{}

// Compile-time check.
var _ languages.LanguageAdapter = (*Adapter)(nil)

// ID returns "python".
func (a *Adapter) ID() string { return "python" }

// DisplayName returns "Python".
func (a *Adapter) DisplayName() string { return "Python" }

// ToolchainCheck looks for python3 on PATH (required for `lernen work`) and
// also probes for pytest and pytest-json-report (non-Required; needed for
// `lernen practice` but must not gate `lernen work`).
func (a *Adapter) ToolchainCheck(_ context.Context) languages.ToolchainStatus {
	tool := languages.ToolStatus{
		Name:     "python3",
		Required: true,
	}
	path, err := exec.LookPath("python3")
	if err == nil {
		tool.Available = true
		tool.Path = path
	} else {
		tool.Hint = "Install Python 3.10+ from https://www.python.org/downloads/, " +
			"`brew install python3` on macOS, or your distribution's package manager."
		tool.Err = err
	}

	pytest := languages.ToolStatus{Name: "pytest", Required: false}
	jsonReport := languages.ToolStatus{Name: "pytest-json-report", Required: false}
	if tool.Available {
		if perr := exec.Command(path, "-c", "import pytest").Run(); perr == nil {
			pytest.Available = true
		} else {
			pytest.Hint = "Needed for `lernen practice`: pip install pytest"
			pytest.Err = perr
		}
		if rerr := exec.Command(path, "-c", "import pytest_jsonreport").Run(); rerr == nil {
			jsonReport.Available = true
		} else {
			jsonReport.Hint = "Needed for `lernen practice`: pip install pytest-json-report"
			jsonReport.Err = rerr
		}
	} else {
		pytest.Hint = "Install python3 first, then: pip install pytest"
		jsonReport.Hint = "Install python3 first, then: pip install pytest-json-report"
	}

	return languages.ToolchainStatus{
		OK:    tool.Available, // python3 only — `lernen work` must not gate on pytest
		Tools: []languages.ToolStatus{tool, pytest, jsonReport},
	}
}

// TestRunner returns the real pytest-driven runner (M4c).
func (a *Adapter) TestRunner() languages.TestRunner {
	return pytestRunner{}
}

// BuildRunner returns the M1 stub. Python has no build step; M3 may use
// `python3 -m py_compile` as a syntax check.
func (a *Adapter) BuildRunner() languages.BuildRunner {
	return languages.UnimplementedBuildRunner{LanguageID: "python"}
}

// REPLCommand returns python3 if it's on PATH; otherwise "" and false.
// The harness uses this to launch a no-AI REPL during practice mode (M4).
func (a *Adapter) REPLCommand() (string, bool) {
	if path, err := exec.LookPath("python3"); err == nil {
		return path, true
	}
	return "", false
}

// Formatter returns ("", false) in the M1 stub; M3 wires ruff format.
func (a *Adapter) Formatter() (string, bool) { return "", false }

// Linter returns ("", false) in the M1 stub; M3 wires ruff check.
func (a *Adapter) Linter() (string, bool) { return "", false }

// CompetencyTaxonomy returns an empty slice in the M1 stub. M3 fills in the
// foundation/fluency/mastery competencies after the forge dogfood pass.
func (a *Adapter) CompetencyTaxonomy() []languages.Competency { return nil }

// SystemPromptAddendum returns the locked Python-specific tutor guidance.
func (a *Adapter) SystemPromptAddendum() string { return addendum }

// CommonErrors returns an empty slice in the M1 stub. M3 adds patterns for
// TypeError on None, IndentationError, NameError, etc.
func (a *Adapter) CommonErrors() []languages.ErrorPattern { return nil }

// DocsLibraryHints returns Context7 IDs the tutor commonly consults for
// Python. These are hints, not exclusivity — the tutor may query other libs.
func (a *Adapter) DocsLibraryHints() []string {
	return []string{
		"/python/cpython",
	}
}

// init registers the Python adapter at package-load time. Per AGENTS.md,
// init() is permitted only for adapter/backend/provider registration, which
// is exactly this case.
//
// Callers activate the adapter by importing this package — typically as a
// blank import alongside the harness's other backends and providers.
func init() {
	languages.Register(&Adapter{})
}
