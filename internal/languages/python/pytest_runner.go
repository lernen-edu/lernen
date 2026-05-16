package python

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/lernen-edu/lernen/internal/languages"
)

// pytestReport is the subset of the pytest-json-report schema we read.
type pytestReport struct {
	Summary struct {
		Total  int `json:"total"`
		Passed int `json:"passed"`
		Failed int `json:"failed"`
	} `json:"summary"`
}

// parsePytestReport maps a pytest-json-report document to a TestResult.
// A document reporting zero collected tests is an infrastructure error
// (an empty or broken scaffold must never read as a pass), as is any
// unparseable input.
func parsePytestReport(reportJSON []byte, rawOutput string) (languages.TestResult, error) {
	var r pytestReport
	if err := json.Unmarshal(reportJSON, &r); err != nil {
		return languages.TestResult{}, fmt.Errorf("python: pytest report unparseable: %w", err)
	}
	if r.Summary.Total == 0 {
		return languages.TestResult{}, fmt.Errorf("python: pytest collected no tests (scaffold ran no test_* functions)")
	}
	return languages.TestResult{
		Total:  r.Summary.Total,
		Passed: r.Summary.Passed,
		Failed: r.Summary.Failed,
		Output: rawOutput,
	}, nil
}

// pytestRunner runs pytest with pytest-json-report in a workdir. It is
// the real languages.TestRunner for Python (replaces the M1 stub).
type pytestRunner struct{}

const pytestOutputCap = 8 << 10 // 8 KiB of tail output for the TUI

// Run executes the test suite in dir. error is reserved for
// infrastructure failure (python3/pytest/json-report absent, exec
// failure, unparseable report, zero tests collected, ctx cancelled).
// A suite that ran with failing tests is a normal TestResult
// (Failed > 0) with a nil error — the caller distinguishes "the
// learner's code is wrong" (record `failed`) from "the environment is
// broken" (record nothing).
func (pytestRunner) Run(ctx context.Context, dir string) (languages.TestResult, error) {
	py, err := exec.LookPath("python3")
	if err != nil {
		return languages.TestResult{}, fmt.Errorf("python: python3 not on PATH: %w", err)
	}
	reportPath := filepath.Join(dir, ".pytest_report.json")
	if rmErr := os.Remove(reportPath); rmErr != nil && !errors.Is(rmErr, fs.ErrNotExist) {
		return languages.TestResult{}, fmt.Errorf("python: cannot clear stale pytest report: %w", rmErr)
	}

	cmd := exec.CommandContext(ctx, py, "-m", "pytest", "-q",
		"-p", "no:cacheprovider",
		"--json-report", "--json-report-file=.pytest_report.json")
	cmd.Dir = dir
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	runErr := cmd.Run()
	if ctx.Err() != nil {
		return languages.TestResult{}, fmt.Errorf("python: pytest cancelled: %w", ctx.Err())
	}

	raw := out.String()
	if len(raw) > pytestOutputCap {
		raw = raw[len(raw)-pytestOutputCap:]
	}

	reportJSON, readErr := os.ReadFile(reportPath)
	if readErr != nil {
		// No report written ⇒ pytest or the json-report plugin is
		// absent / failed to start. Infra error, not a test failure.
		return languages.TestResult{}, fmt.Errorf(
			"python: pytest produced no JSON report (is pytest-json-report installed?): run error: %v\noutput:\n%s",
			runErr, raw)
	}
	return parsePytestReport(reportJSON, raw)
}
