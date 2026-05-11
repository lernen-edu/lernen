package completion

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/lernen-edu/lernen/internal/backends"
	"github.com/lernen-edu/lernen/internal/curriculum"
	"github.com/lernen-edu/lernen/internal/progress"
)

// StructureCompletion runs the chapter-completion structurer against a
// transcript. Retries once if the first response fails to parse or
// fails Validate; tightens the prompt on retry. Returns the populated
// ChapterCompletion (note: tier_demonstrated is the structurer's
// claim; the harness can cross-check against competencies[*].Tier
// after).
func StructureCompletion(
	ctx context.Context,
	be backends.Backend,
	transcript string,
	chapter *curriculum.Chapter,
	competencies []curriculum.Competency,
) (*progress.ChapterCompletion, error) {
	system := StructurerSystemPrompt()
	userMsg := buildUserMessage(transcript, chapter, competencies, false)

	resp, err := be.Chat(ctx, []backends.Message{{Role: backends.RoleUser, Content: userMsg}}, system)
	if err != nil {
		return nil, fmt.Errorf("completion: structurer first call: %w", err)
	}
	cc, parseErr := parseStructurerOutput(resp.Content)
	if parseErr == nil {
		if valErr := cc.Validate(false); valErr == nil {
			return cc, nil
		} else {
			parseErr = valErr
		}
	}

	tightened := buildUserMessage(transcript, chapter, competencies, true) +
		"\n\n# Previous attempt failed\n\n" +
		"The previous attempt failed with: " + parseErr.Error() +
		"\nEmit ONLY the YAML block. No preamble. Required fields per the system prompt."
	resp2, err := be.Chat(ctx, []backends.Message{{Role: backends.RoleUser, Content: tightened}}, system)
	if err != nil {
		return nil, fmt.Errorf("completion: structurer retry call: %w", err)
	}
	cc2, parseErr2 := parseStructurerOutput(resp2.Content)
	if parseErr2 != nil {
		return nil, fmt.Errorf("completion: structurer output unparseable after retry: %w (raw output: %q)", parseErr2, resp2.Content)
	}
	if valErr := cc2.Validate(false); valErr != nil {
		return nil, fmt.Errorf("completion: structurer output failed validation after retry: %w", valErr)
	}
	return cc2, nil
}

// MissingCompetencies returns the chapter's introduced competencies
// that the structurer did NOT include in its demonstrations list.
// Empty when the structurer accounted for every introduced competency.
func MissingCompetencies(cc *progress.ChapterCompletion, introduced []string) []string {
	if cc.Kind != "content" {
		return nil
	}
	have := make(map[string]struct{}, len(cc.Demonstrations))
	for _, d := range cc.Demonstrations {
		have[d.CompetencyID] = struct{}{}
	}
	var gap []string
	for _, id := range introduced {
		if _, ok := have[id]; !ok {
			gap = append(gap, id)
		}
	}
	return gap
}

func buildUserMessage(transcript string, chapter *curriculum.Chapter, comps []curriculum.Competency, retry bool) string {
	var b strings.Builder
	b.WriteString("# Chapter metadata\n\n")
	fmt.Fprintf(&b, "chapter_id: %s\n", chapter.ID)
	fmt.Fprintf(&b, "title: %s\n", chapter.Title)
	kind := "content"
	if len(chapter.CompetenciesIntroduced) == 0 {
		kind = "orientation"
	}
	fmt.Fprintf(&b, "kind: %s\n\n", kind)

	if kind == "content" {
		b.WriteString("# Competencies introduced (with authored tier)\n\n")
		introduced := make(map[string]struct{}, len(chapter.CompetenciesIntroduced))
		for _, id := range chapter.CompetenciesIntroduced {
			introduced[id] = struct{}{}
		}
		for _, c := range comps {
			if _, ok := introduced[c.ID]; !ok {
				continue
			}
			fmt.Fprintf(&b, "- id: %s\n  tier: %s\n  name: %s\n  description: %s\n",
				c.ID, c.Tier, c.Name, strings.ReplaceAll(c.Description, "\n", " "))
		}
		if len(chapter.Exercises) > 0 {
			b.WriteString("\n# Authored exercises (the user may or may not have attempted these)\n\n")
			for _, ex := range chapter.Exercises {
				fmt.Fprintf(&b, "- id: %s\n  prompt: %s\n  competencies: %v\n",
					ex.ID, strings.ReplaceAll(ex.Prompt, "\n", " "), ex.Competencies)
			}
		}
	}

	b.WriteString("\n# Conversation transcript\n\n")
	b.WriteString(transcript)

	if retry {
		b.WriteString("\n\n# IMPORTANT\n")
		b.WriteString("Return ONLY the fenced YAML block. No preamble. No commentary.")
	}
	return b.String()
}

var yamlFenceRe = regexp.MustCompile("(?s)```yaml\\s*\\n(.*?)\\n```")

func parseStructurerOutput(raw string) (*progress.ChapterCompletion, error) {
	m := yamlFenceRe.FindStringSubmatch(raw)
	if len(m) != 2 {
		return nil, fmt.Errorf("missing yaml block (looked for ```yaml ... ```)")
	}
	var cc progress.ChapterCompletion
	if err := yaml.Unmarshal([]byte(m[1]), &cc); err != nil {
		return nil, fmt.Errorf("yaml unmarshal: %w", err)
	}
	return &cc, nil
}
