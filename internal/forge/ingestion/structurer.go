package ingestion

import (
	"context"
	"fmt"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/lernen-edu/lernen/internal/backends"
)

// Structure runs the call-2 structuring dispatch for Stage 3. Sends
// the Stage 3 transcript to the backend with StructurerSystemPrompt
// as system, parses the YAML response, stamps AuthoredAt with the
// current UTC time, and validates. Returns the parsed Ingestion on
// success or an error with the raw model output embedded for diagnosis
// on parse/validate failure (mirrors recommendation/structurer.go).
func Structure(ctx context.Context, be backends.Backend, transcript string) (*Ingestion, error) {
	req := []backends.Message{
		{Role: backends.RoleUser, Content: transcript},
	}
	resp, err := be.Chat(ctx, req, StructurerSystemPrompt())
	if err != nil {
		return nil, fmt.Errorf("ingestion: structurer call: %w", err)
	}
	raw := stripCodeFence(resp.Content)

	var ing Ingestion
	if err := yaml.Unmarshal([]byte(raw), &ing); err != nil {
		return nil, fmt.Errorf("ingestion: structurer unmarshal: %w; raw output: %s", err, raw)
	}
	ing.SchemaVersion = CurrentSchemaVersion
	ing.AuthoredAt = time.Now().UTC()

	if err := ing.Validate(); err != nil {
		return nil, fmt.Errorf("ingestion: structurer validate: %w; raw output: %s", err, raw)
	}
	return &ing, nil
}

// stripCodeFence removes a single leading/trailing markdown code fence
// pair if present (e.g., "```yaml\n...\n```"). Mirrors the same helper
// in goals/structurer.go, calibration/structurer.go, and
// recommendation/structurer.go. Kept package-local so ingestion
// remains a leaf.
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
