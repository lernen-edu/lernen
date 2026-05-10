package goals

import (
	"context"
	"fmt"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/lernen-edu/lernen/internal/backends"
)

// Structure runs the call-2 structuring step. It sends the structurer
// system prompt and the conversation transcript to the backend
// (non-streaming Chat call), parses the response as YAML directly, and
// runs Validate() against the parsed Goals.
//
// The structurer.md prompt constrains the model to bare YAML, but as a
// robustness layer Structure also accepts a single outer ```/```yaml
// fence — some models wrap output despite the instruction.
//
// The returned *Goals is non-nil and Validate-passing on success. On any
// failure (backend error, malformed YAML, validation failure) the
// returned *Goals is nil and the error message includes enough context
// to diagnose — for malformed YAML, the raw output appears in the error
// so callers can surface it.
//
// Structure stamps AuthoredAt with the current UTC time on success
// (overriding whatever the model produced). This guarantees the
// timestamp is honest regardless of the model's clock awareness.
func Structure(ctx context.Context, be backends.Backend, transcript string) (*Goals, error) {
	msgs := []backends.Message{
		{Role: backends.RoleUser, Content: transcript},
	}
	resp, err := be.Chat(ctx, msgs, StructurerSystemPrompt())
	if err != nil {
		return nil, fmt.Errorf("goals: structuring call failed: %w", err)
	}
	raw := stripCodeFence(resp.Content)

	var g Goals
	if err := yaml.Unmarshal([]byte(raw), &g); err != nil {
		return nil, fmt.Errorf("goals: structuring call returned malformed YAML: %w\n--- raw output ---\n%s", err, raw)
	}
	g.AuthoredAt = time.Now().UTC()
	if err := g.Validate(); err != nil {
		return nil, fmt.Errorf("goals: structuring call output failed validation: %w\n--- raw output ---\n%s", err, raw)
	}
	return &g, nil
}

// stripCodeFence removes a single outer triple-backtick fence from s
// if present. Handles "```" and "```<language>" opening fences, with
// or without a trailing closing "```". Whitespace around the fence is
// trimmed. If s does not start with "```", it is returned with only
// surrounding whitespace trimmed.
//
// This is a robustness layer for the call-2 structuring response.
// structurer.md instructs the model to emit bare YAML, but in
// practice some models wrap it anyway; rather than fail to parse a
// recoverable response, we peel off the outer fence and proceed.
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
