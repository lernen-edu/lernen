package reflection

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/lernen-edu/lernen/internal/backends"
)

// ReflectionDefaults carries the contextual inputs to the structurer
// that come from outside the transcript: the default curriculum-id
// derived from Recommendation.CurriculumName, and a markdown rendering
// of the loaded forge state the structurer needs to compose forge_log.md.
type ReflectionDefaults struct {
	DefaultCurriculumID   string
	DefaultCurriculumName string
	// ForgeStateMarkdown is a paraphrasable rendering of the six loaded
	// profile artifacts in the heading shape the structurer must
	// produce. The orchestrator builds this with renderForgeStateMarkdown
	// (a helper in this package); StructureReflection passes it through
	// as part of the user message.
	ForgeStateMarkdown string
}

// StructureReflection runs the reflection structurer against the
// transcript. Retries once if the first response fails to parse or
// fails Validate; tightens the prompt on retry.
func StructureReflection(ctx context.Context, be backends.Backend, transcript string, defaults ReflectionDefaults) (*ReflectionResult, error) {
	system := ReflectionStructurerSystemPrompt()
	userMsg := buildStructurerUserMessage(transcript, defaults, false)

	resp, err := be.Chat(ctx, []backends.Message{{Role: backends.RoleUser, Content: userMsg}}, system)
	if err != nil {
		return nil, fmt.Errorf("reflection: structurer first call: %w", err)
	}
	res, parseErr := parseStructurerOutput(resp.Content, defaults)
	if parseErr == nil {
		if valErr := res.Validate(); valErr == nil {
			return res, nil
		} else {
			parseErr = valErr
		}
	}

	// Retry with a tightened user message that surfaces the first failure.
	tightenedUserMsg := buildStructurerUserMessage(transcript, defaults, true) +
		"\n\n# Previous attempt failed\n\n" +
		"The previous attempt failed with: " + parseErr.Error() +
		"\nEmit only the YAML block and the fenced markdown block. No preamble. Every required heading must be present."
	resp2, err := be.Chat(ctx, []backends.Message{{Role: backends.RoleUser, Content: tightenedUserMsg}}, system)
	if err != nil {
		return nil, fmt.Errorf("reflection: structurer retry call: %w", err)
	}
	res2, parseErr2 := parseStructurerOutput(resp2.Content, defaults)
	if parseErr2 != nil {
		return nil, fmt.Errorf("reflection: structurer output unparseable after retry: %w (raw output: %q)", parseErr2, resp2.Content)
	}
	if valErr := res2.Validate(); valErr != nil {
		return nil, fmt.Errorf("reflection: structurer output failed validation after retry: %w", valErr)
	}
	return res2, nil
}

func buildStructurerUserMessage(transcript string, defaults ReflectionDefaults, retry bool) string {
	var b strings.Builder
	b.WriteString("# Default curriculum-id (use unless user proposed an alternative)\n\n")
	b.WriteString(defaults.DefaultCurriculumID)
	b.WriteString("\n\n# Default curriculum name (use unless user proposed an alternative)\n\n")
	b.WriteString(defaults.DefaultCurriculumName)
	b.WriteString("\n\n# Loaded forge state (for the markdown sections)\n\n")
	b.WriteString(defaults.ForgeStateMarkdown)
	b.WriteString("\n\n# Reflection transcript\n\n")
	b.WriteString(transcript)
	if retry {
		b.WriteString("\n\n# IMPORTANT")
		b.WriteString("\nReturn ONLY the YAML block followed by the fenced markdown block. No preamble. No commentary between blocks.")
	}
	return b.String()
}

var (
	yamlFenceRe = regexp.MustCompile("(?s)```yaml\\s*\\n(.*?)\\n```")
	mdFenceRe   = regexp.MustCompile("(?s)```markdown\\s*\\n(.*?)\\n```")
)

func parseStructurerOutput(raw string, defaults ReflectionDefaults) (*ReflectionResult, error) {
	ymlMatch := yamlFenceRe.FindStringSubmatch(raw)
	if len(ymlMatch) != 2 {
		return nil, fmt.Errorf("missing yaml block (looked for ```yaml ... ```)")
	}
	mdMatch := mdFenceRe.FindStringSubmatch(raw)
	if len(mdMatch) != 2 {
		return nil, fmt.Errorf("missing markdown block (looked for ```markdown ... ```)")
	}
	var out ReflectionResult
	if err := yaml.Unmarshal([]byte(ymlMatch[1]), &out); err != nil {
		return nil, fmt.Errorf("yaml unmarshal: %w", err)
	}
	out.ForgeLog = strings.TrimSpace(mdMatch[1]) + "\n"
	// Fill the curriculum from defaults if the structurer omitted it.
	if strings.TrimSpace(out.Curriculum.ID) == "" {
		out.Curriculum.ID = defaults.DefaultCurriculumID
	}
	if strings.TrimSpace(out.Curriculum.Name) == "" {
		out.Curriculum.Name = defaults.DefaultCurriculumName
	}
	return &out, nil
}
