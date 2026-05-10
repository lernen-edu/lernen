package recommendation

import (
	"context"
	"fmt"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/lernen-edu/lernen/internal/backends"
)

// Structure runs the call-2 structuring step for Stage 2. It sends
// the structurer system prompt (parametrized with adapterIDs so the
// model knows the valid-language set) and the recommendation
// transcript to the backend (non-streaming Chat call), parses the
// response as YAML directly, and runs Validate() against the parsed
// Recommendation. Validate calls into the live LanguageAdapter
// registry for the language ID check.
//
// As a robustness layer Structure also accepts a single outer
// ```/```yaml fence — some models wrap output despite the prompt's
// strict instruction.
//
// On success: returns a non-nil *Recommendation that has passed
// Validate, with AuthoredAt stamped to the current UTC time
// (overriding whatever the model produced — guarantees the
// timestamp is honest regardless of the model's clock awareness).
//
// On failure: returns (nil, err). All three failure modes — backend
// error, malformed YAML, and validation failure — return nil. For
// malformed YAML and validation failures the error message includes
// the raw model output so callers can surface it.
func Structure(ctx context.Context, be backends.Backend, transcript string, adapterIDs []string) (*Recommendation, error) {
	msgs := []backends.Message{
		{Role: backends.RoleUser, Content: transcript},
	}
	resp, err := be.Chat(ctx, msgs, StructurerSystemPrompt(adapterIDs))
	if err != nil {
		return nil, fmt.Errorf("recommendation: structuring call failed: %w", err)
	}
	raw := stripCodeFence(resp.Content)

	var rec Recommendation
	if err := yaml.Unmarshal([]byte(raw), &rec); err != nil {
		return nil, fmt.Errorf("recommendation: structuring call returned malformed YAML: %w\n--- raw output ---\n%s", err, raw)
	}
	rec.AuthoredAt = time.Now().UTC()
	if err := rec.Validate(); err != nil {
		return nil, fmt.Errorf("recommendation: structuring call output failed validation: %w\n--- raw output ---\n%s", err, raw)
	}
	return &rec, nil
}

// stripCodeFence removes a single outer triple-backtick fence from s
// if present. Handles "```" and "```<language>" opening fences, with
// or without a trailing closing "```". Whitespace around the fence is
// trimmed. If s does not start with "```", returns it with only
// surrounding whitespace trimmed.
//
// Mirrors goals/structurer.go's and calibration/structurer.go's
// stripCodeFence — duplicated rather than imported to keep
// recommendation a leaf that doesn't depend on either sibling
// package's internals.
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
