package gate

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	_ "embed"

	"github.com/lernen-edu/lernen/internal/backends"
	"github.com/lernen-edu/lernen/internal/languages"
	"gopkg.in/yaml.v3"
)

//go:embed comprehension_evaluator.md
var comprehensionEvaluatorPrompt string

var compYamlRe = regexp.MustCompile("(?s)```yaml\\s*\\n(.*?)\\n```")

type comprehensionVerdict struct {
	Matches bool `yaml:"matches"`
}

func normalizeOutput(s string) string {
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " \t")
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// RunComprehensionSample grades one sample: exact-match output
// (offline) AND a conservative evaluator match for the free-text
// issue. Evaluator failure => infra (non-terminal, spec §2.4).
func RunComprehensionSample(ctx context.Context, be backends.Backend, cf languages.ComprehensionFixture, predictedOutput, issueClaim string) (ComponentOutcome, error) {
	if normalizeOutput(predictedOutput) != normalizeOutput(cf.ExpectedOutput) {
		return OutcomeFail, nil // objective miss; do not spend a backend call
	}
	match, err := evaluateIssue(ctx, be, cf, issueClaim)
	if err != nil {
		return OutcomeInfraError, err
	}
	if !match {
		return OutcomeFail, nil
	}
	return OutcomePass, nil
}

func evaluateIssue(ctx context.Context, be backends.Backend, cf languages.ComprehensionFixture, claim string) (bool, error) {
	var key strings.Builder
	for _, e := range cf.ExpectedIssues {
		fmt.Fprintf(&key, "- %s\n", e.Description)
	}
	user := fmt.Sprintf("SNIPPET:\n```\n%s\n```\n\nANSWER KEY (real defects):\n%s\nLEARNER CLAIM:\n%s\n", cf.Snippet, key.String(), claim)
	v, err := callEvaluatorOnce(ctx, be, user)
	if err == nil {
		return v.Matches, nil
	}
	// One tightened retry, mirroring the explain-back/completion pattern.
	v, err = callEvaluatorOnce(ctx, be, user+"\n\n# Previous attempt failed\n\nRespond with ONLY the fenced yaml block:\n```yaml\nmatches: true_or_false\n```")
	if err != nil {
		return false, err
	}
	return v.Matches, nil
}

func callEvaluatorOnce(ctx context.Context, be backends.Backend, user string) (comprehensionVerdict, error) {
	resp, err := be.Chat(ctx, []backends.Message{{Role: backends.RoleUser, Content: user}}, comprehensionEvaluatorPrompt)
	if err != nil {
		return comprehensionVerdict{}, err
	}
	m := compYamlRe.FindStringSubmatch(resp.Content)
	if m == nil {
		return comprehensionVerdict{}, fmt.Errorf("gate: comprehension evaluator: no yaml block")
	}
	var v comprehensionVerdict
	if err := yaml.Unmarshal([]byte(m[1]), &v); err != nil {
		return comprehensionVerdict{}, fmt.Errorf("gate: comprehension evaluator: parse: %w", err)
	}
	return v, nil
}
