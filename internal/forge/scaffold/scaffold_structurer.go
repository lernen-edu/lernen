package scaffold

import (
	"context"
	"fmt"

	"gopkg.in/yaml.v3"

	"github.com/lernen-edu/lernen/internal/backends"
)

// StructureChapter runs the Pass 2 structuring step for one chapter.
// Sends the scaffold_structurer system prompt (with chapterID and kind
// interpolated) and the chapter's sub-transcript to the backend
// (non-streaming Chat call), parses the response.
//
// For kind=orientation, the response is a single ChapterScaffold YAML
// body. For kind=content, the response is a two-key document:
//
//	scaffold: <ChapterScaffold body>
//	new_competencies: <[]Competency>
//
// On success: returns (scaffold, newCompetencies, nil) where scaffold
// has passed Validate and each newCompetency has passed Validate.
//
// On failure: returns (nil, nil, err). All failure modes — backend
// error, malformed YAML, kind/id mismatch, validation failure — return
// nil/nil. For YAML and validation failures the error message includes
// the raw model output.
func StructureChapter(ctx context.Context, be backends.Backend, chapterID, kind, subTranscript string) (*ChapterScaffold, []Competency, error) {
	msgs := []backends.Message{
		{Role: backends.RoleUser, Content: subTranscript},
	}
	resp, err := be.Chat(ctx, msgs, ScaffoldStructurerSystemPrompt(chapterID, kind))
	if err != nil {
		return nil, nil, fmt.Errorf("scaffold: chapter structuring call failed: %w", err)
	}
	raw := stripCodeFence(resp.Content)

	var scaff *ChapterScaffold
	var comps []Competency

	switch kind {
	case "orientation":
		var s ChapterScaffold
		if err := yaml.Unmarshal([]byte(raw), &s); err != nil {
			return nil, nil, fmt.Errorf("scaffold: orientation chapter structuring returned malformed YAML: %w\n--- raw ---\n%s", err, raw)
		}
		scaff = &s
	case "content":
		var doc struct {
			Scaffold        ChapterScaffold `yaml:"scaffold"`
			NewCompetencies []Competency    `yaml:"new_competencies"`
		}
		if err := yaml.Unmarshal([]byte(raw), &doc); err != nil {
			return nil, nil, fmt.Errorf("scaffold: content chapter structuring returned malformed YAML: %w\n--- raw ---\n%s", err, raw)
		}
		scaff = &doc.Scaffold
		comps = doc.NewCompetencies
	default:
		return nil, nil, fmt.Errorf("scaffold: StructureChapter: unknown kind %q", kind)
	}

	// Cross-check: structurer's id and kind must match what the caller asked for.
	if scaff.ID != chapterID {
		return nil, nil, fmt.Errorf("scaffold: chapter structuring id mismatch: got %q; want %q\n--- raw ---\n%s", scaff.ID, chapterID, raw)
	}
	if scaff.Kind != kind {
		return nil, nil, fmt.Errorf("scaffold: chapter structuring kind mismatch: got %q; want %q\n--- raw ---\n%s", scaff.Kind, kind, raw)
	}

	if err := scaff.Validate(); err != nil {
		return nil, nil, fmt.Errorf("scaffold: chapter structuring scaffold failed validation: %w\n--- raw ---\n%s", err, raw)
	}
	for i := range comps {
		if err := comps[i].Validate(); err != nil {
			return nil, nil, fmt.Errorf("scaffold: chapter structuring new_competencies[%d] failed validation: %w\n--- raw ---\n%s", i, err, raw)
		}
	}

	return scaff, comps, nil
}
