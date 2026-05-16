// Package languages defines the LanguageAdapter interface and the registry
// of installed adapters.
//
// LanguageAdapter encapsulates everything the harness needs to know about a
// specific programming language the learner is studying — toolchain checks,
// the test runner, system-prompt guidance, the language-specific competency
// taxonomy, common error patterns the tutor should recognize.
//
// v0 ships one adapter (Python). Additional adapters register themselves at
// compile time via init() in their own packages; see PRD §4.2 and
// docs/PRE_BUILD_ANSWERS.md §11 for the v0 design.
package languages

import (
	"context"
)

// LanguageAdapter is the harness's plug-in point for a given programming
// language. v0 implementations: internal/languages/python.
//
// In M1, only ID, DisplayName, ToolchainCheck, and SystemPromptAddendum are
// exercised by the runtime. The full interface is defined now so that future
// milestones can fill in the remaining methods without changing call sites.
type LanguageAdapter interface {
	// ID is a stable lowercase identifier used in manifests, logs, and the
	// competency-namespacing convention (e.g. "python", "go", "rust").
	ID() string

	// DisplayName is the human-facing name used in TUI text and prompts
	// (e.g. "Python", "Go", "Rust").
	DisplayName() string

	// ToolchainCheck probes the learner's environment for the tools this
	// adapter requires. It must be cheap (no network, no model spend) and
	// suitable to call during `lernen setup` and at every `lernen work`
	// invocation.
	ToolchainCheck(ctx context.Context) ToolchainStatus

	// TestRunner returns the runner the harness uses to execute the
	// learner's exercise solutions and parse the results. Stub adapters in
	// M1 return UnimplementedTestRunner.
	TestRunner() TestRunner

	// BuildRunner returns the build step (no-op for interpreted languages).
	// Stub adapters in M1 return UnimplementedBuildRunner.
	BuildRunner() BuildRunner

	// GateFixtures returns this language's embedded capability-gate
	// fixture bank (M5a). It is loaded/validated at call time so a
	// malformed bank surfaces as an error, never a panic.
	GateFixtures() (GateFixtures, error)

	// REPLCommand returns the interactive REPL the adapter recommends
	// (e.g. "python3"). available is false when the REPL is not on PATH.
	REPLCommand() (cmd string, available bool)

	// Formatter returns the canonical formatter (e.g. "ruff format" for
	// Python). available reflects ToolchainCheck.
	Formatter() (cmd string, available bool)

	// Linter returns the canonical linter (e.g. "ruff check" for Python).
	// available reflects ToolchainCheck.
	Linter() (cmd string, available bool)

	// CompetencyTaxonomy returns the language-specific competencies the
	// tutor and the forge will compose with the universal taxonomy. Stub
	// adapters in M1 may return an empty slice; M3 fills it in.
	CompetencyTaxonomy() []Competency

	// SystemPromptAddendum returns the language-specific guidance appended
	// to Phase1TutorSystemPrompt at session start. Tells the tutor what
	// idioms and traps to watch for in this language.
	SystemPromptAddendum() string

	// CommonErrors returns patterns the tutor should recognize when the
	// learner pastes an error message (e.g., Python's TypeError on None).
	// Stub adapters in M1 return an empty slice.
	CommonErrors() []ErrorPattern

	// DocsLibraryHints returns Context7 library IDs the tutor most often
	// consults for this language (e.g., "/python/cpython"). The
	// DocsProvider uses these as the default search space.
	DocsLibraryHints() []string
}

// ToolchainStatus is the result of ToolchainCheck. OK is true iff every
// required tool was found.
type ToolchainStatus struct {
	OK    bool
	Tools []ToolStatus
}

// ToolStatus describes whether a single tool meets the adapter's
// requirements. Path and Version are populated when the tool is found.
type ToolStatus struct {
	Name      string // canonical tool name (e.g. "python3", "pytest")
	Required  bool   // true when missing this tool fails ToolchainCheck.OK
	Available bool   // true when found on PATH and (if applicable) at a usable version
	Path      string // resolved absolute path on success; empty otherwise
	Version   string // observed version string when known
	Hint      string // user-facing install instruction (e.g. "brew install python3")
	Err       error  // additional detail when Available is false
}

// Competency is one entry in the language-specific competency taxonomy.
// Universal competencies live in the harness; the adapter contributes a
// language-specific layer (e.g. "python:list-mutation") and the manifest may
// add manifest-specific entries.
type Competency struct {
	ID                  string // language-prefixed: e.g. "python:list-mutation"
	Name                string
	Description         string
	Tier                CompetencyTier
	ObservableBehaviors []string
}

// CompetencyTier classifies how deeply the learner must hold a competency.
// See PRD §4.7.
type CompetencyTier string

const (
	TierFoundation CompetencyTier = "foundation"
	TierFluency    CompetencyTier = "fluency"
	TierMastery    CompetencyTier = "mastery"
)

// ErrorPattern is one common-mistake signature the tutor recognizes from
// pasted error output. Pattern is a regex fragment.
type ErrorPattern struct {
	Name        string
	Pattern     string
	Description string
	Hint        string
}

// TestRunner runs an exercise solution's tests in dir and parses the result.
// Real implementations land in M3 (e.g., pytest with the json-report plugin).
type TestRunner interface {
	Run(ctx context.Context, dir string) (TestResult, error)
}

// BuildRunner is the language-specific build/compile step (a no-op for
// interpreted languages; `go build` for Go; `cargo build` for Rust).
type BuildRunner interface {
	Build(ctx context.Context, dir string) error
}

// TestResult summarizes a TestRunner.Run invocation.
type TestResult struct {
	Total  int
	Passed int
	Failed int
	Output string // raw test-runner stdout/stderr for surfacing in the TUI
}
