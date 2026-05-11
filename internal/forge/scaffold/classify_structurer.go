package scaffold

import (
	"context"
	"fmt"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/lernen-edu/lernen/internal/backends"
)

// StructureClassification runs the Pass 1 structuring step. Sends the
// classify_structurer system prompt (with chapterIDs interpolated so
// the model knows the required chapter id set) and the dialogue
// transcript to the backend (non-streaming Chat call), parses the
// response as YAML, stamps AuthoredAt to the current UTC time, and
// runs Validate against the parsed ClassifiedChapters.
//
// As a robustness layer, also accepts a single outer ```/```yaml fence
// — some models wrap output despite the strict prompt instruction.
//
// On success: returns a non-nil *ClassifiedChapters that has passed
// Validate.
//
// On failure: returns (nil, err). Backend errors, malformed YAML, and
// validation failures all return nil. For malformed YAML and
// validation failures the error message includes the raw model output.
func StructureClassification(ctx context.Context, be backends.Backend, transcript string, chapterIDs []string) (*ClassifiedChapters, error) {
	msgs := []backends.Message{
		{Role: backends.RoleUser, Content: transcript},
	}
	resp, err := be.Chat(ctx, msgs, ClassifyStructurerSystemPrompt(chapterIDs))
	if err != nil {
		return nil, fmt.Errorf("scaffold: classify structuring call failed: %w", err)
	}
	raw := stripCodeFence(resp.Content)
	var cc ClassifiedChapters
	if err := yaml.Unmarshal([]byte(raw), &cc); err != nil {
		return nil, fmt.Errorf("scaffold: classify structuring call returned malformed YAML: %w\n--- raw output ---\n%s", err, raw)
	}
	cc.AuthoredAt = time.Now().UTC()
	if err := cc.Validate(); err != nil {
		return nil, fmt.Errorf("scaffold: classify structuring call output failed validation: %w\n--- raw output ---\n%s", err, raw)
	}
	return &cc, nil
}

// stripCodeFence removes a single outer triple-backtick fence from s
// if present. Handles "```" and "```<language>" opening fences, with
// or without a trailing closing "```". Whitespace around the fence is
// trimmed. If s does not start with "```", returns it with only
// surrounding whitespace trimmed.
//
// Mirrors recommendation/structurer.go's stripCodeFence — duplicated
// rather than imported to keep scaffold a leaf package that doesn't
// depend on sibling internals.
func stripCodeFence(s string) string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "```") {
		return s
	}
	// Strip the opening fence line (everything up to and including
	// the first newline).
	if idx := strings.IndexByte(s, '\n'); idx >= 0 {
		s = s[idx+1:]
	} else {
		// No newline — the entire string was just "```<something>".
		// Return empty rather than guessing.
		return ""
	}
	// Strip a trailing closing fence.
	s = strings.TrimRight(s, " \t\n")
	if strings.HasSuffix(s, "```") {
		s = strings.TrimSuffix(s, "```")
	}
	return strings.TrimSpace(s)
}
