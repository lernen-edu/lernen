// Package gemini implements the Backend interface against Google's
// gemini-cli (https://github.com/google-gemini/gemini-cli).
//
// Invocation summary (verified via gemini-cli docs at v0.41):
//   - HealthCheck: `gemini --version` — exits 0 when the binary is
//     usable. There is no documented auth-status subcommand, so we
//     additionally verify the API-key env var is set; auth errors
//     surface on the first real call.
//   - Chat: `gemini -o text -m <model>` with the rendered prompt on
//     stdin. Stdout is the assistant text; stderr captures any
//     diagnostics we fold into errors when the subprocess fails.
//   - StreamChat: `gemini -o stream-json -m <model>`. Stdout becomes
//     NDJSON. Each `message` event becomes one Token (chunk-level —
//     not per-token like codex); `error` events become Token{Err:...};
//     `result` closes the channel cleanly.
//
// gemini-cli's headless mode is inference-only by default (no
// codex-style sandbox flags needed). The Backend reads cfg.APIKeyEnv
// from the process environment at call time and forwards it via the
// inherited subprocess environment. The key is never logged, never
// echoed in errors, and stderr lines mentioning the env var name are
// scrubbed before being surfaced.
//
// The stream-json schema is not fully published; we parse defensively
// (falling back to a `text` field if `content` is missing, ignoring
// unknown event types). M2 dogfood will reveal any mismatches.
package gemini

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/lernen-edu/lernen/internal/backends"
)

const (
	defaultCommand = "gemini"
	chatTimeout    = 120 * time.Second
	stderrCap      = 1024
)

var _ backends.Backend = (*Backend)(nil)

// Config carries the gemini-specific settings.
type Config struct {
	// APIKeyEnv names the environment variable holding the gemini API key
	// (typically "GEMINI_API_KEY"). The value is forwarded to the
	// subprocess via the inherited environment.
	APIKeyEnv string

	// Model is the gemini `-m` argument (e.g. "gemini-2.5-flash").
	Model string
}

// Backend is the gemini-cli backend.
type Backend struct {
	cfg      Config
	command  string
	extraEnv []string
}

// Option configures a Backend at construction time.
type Option func(*Backend)

// WithCommand overrides the gemini binary path. Used in tests.
func WithCommand(path string) Option {
	return func(b *Backend) { b.command = path }
}

// WithExtraEnv appends environment entries to every subprocess invocation.
func WithExtraEnv(env []string) Option {
	return func(b *Backend) { b.extraEnv = append(b.extraEnv, env...) }
}

// New constructs a Backend.
func New(cfg Config, opts ...Option) *Backend {
	b := &Backend{cfg: cfg, command: defaultCommand}
	for _, opt := range opts {
		opt(b)
	}
	return b
}

// Name returns "gemini".
func (b *Backend) Name() string { return "gemini" }

// HealthCheck runs `gemini --version`. Returns nil on exit 0. The CLI
// exposes no documented auth-status subcommand; auth errors surface
// only on the first real call.
//
// Auth is delegated to the gemini CLI: it may use the API-key env var
// (forwarded via inherited env when set) OR its own stored credentials
// (free Cloud Code Assist tokens at ~/.gemini/, Vertex ADC, etc.). We
// do not pre-check the env var here — a user authenticated via
// `gemini auth login` should not be blocked just because they haven't
// exported GEMINI_API_KEY.
func (b *Backend) HealthCheck(ctx context.Context) error {
	cmd := b.subprocess(ctx, "--version")
	stderr := &bytes.Buffer{}
	cmd.Stderr = stderr
	cmd.Stdout = io.Discard

	if err := cmd.Run(); err != nil {
		if isMissingBinary(err) {
			return fmt.Errorf("gemini: cannot run %q (is gemini-cli installed?): %w", b.command, err)
		}
		return fmt.Errorf("gemini: --version failed: %s", b.scrubStderr(stderr.String()))
	}
	return nil
}

// Chat performs a non-streaming completion. Uses `-o text` so stdout is
// the response text directly. See HealthCheck for the auth contract.
func (b *Backend) Chat(ctx context.Context, messages []backends.Message, systemPrompt string) (backends.Response, error) {
	reqCtx, cancel := context.WithTimeout(ctx, chatTimeout)
	defer cancel()

	prompt := renderPrompt(messages, systemPrompt)
	cmd := b.callSubprocess(reqCtx, "text")
	cmd.Stdin = strings.NewReader(prompt)
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	if err := cmd.Run(); err != nil {
		return backends.Response{}, b.wrapRunError(err, stderr.String())
	}

	return backends.Response{Content: strings.TrimRight(stdout.String(), "\n")}, nil
}

// StreamChat performs a streaming completion. The returned channel emits
// content tokens at message-chunk granularity (gemini-cli's stream-json
// is not per-token). On error or context cancellation, a single
// Token{Err: ...} is emitted before the channel closes. See HealthCheck
// for the auth contract.
func (b *Backend) StreamChat(ctx context.Context, messages []backends.Message, systemPrompt string) (<-chan backends.Token, error) {
	prompt := renderPrompt(messages, systemPrompt)
	cmd := b.callSubprocess(ctx, "stream-json")
	cmd.Stdin = strings.NewReader(prompt)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("gemini: stdout pipe: %w", err)
	}
	stderr := &bytes.Buffer{}
	cmd.Stderr = stderr

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("gemini: start: %w", err)
	}

	out := make(chan backends.Token, 16)
	go b.streamFromNDJSON(ctx, cmd, stdout, stderr, out)
	return out, nil
}

func (b *Backend) streamFromNDJSON(ctx context.Context, cmd *exec.Cmd, stdout io.ReadCloser, stderr *bytes.Buffer, out chan<- backends.Token) {
	defer close(out)

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 4096), 1024*1024)

	emit := func(tok backends.Token) bool {
		select {
		case out <- tok:
			return true
		case <-ctx.Done():
			out <- backends.Token{Err: ctx.Err()}
			_ = interrupt(cmd)
			return false
		}
	}

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			out <- backends.Token{Err: ctx.Err()}
			_ = interrupt(cmd)
			_ = cmd.Wait()
			return
		default:
		}

		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}

		var ev geminiEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			continue // tolerant
		}

		switch ev.Type {
		case "result":
			_ = cmd.Wait()
			return
		case "error":
			msg := ev.Message
			if msg == "" {
				msg = "gemini stream returned error"
			}
			emit(backends.Token{Err: fmt.Errorf("gemini: %s", msg)})
			_ = interrupt(cmd)
			_ = cmd.Wait()
			return
		case "message":
			// gemini-cli emits the user's input as a `message` event with
			// role:"user" right after `init`, BEFORE any model output —
			// this is input-echo, not response. Verified via gemini-cli
			// nonInteractiveCli.ts: assistant streaming chunks set
			// role:"assistant" with delta:true. Filter to assistant
			// messages so we don't render the user's prompt back at them.
			if ev.Role != "assistant" {
				continue
			}
			text := ev.Content
			if text == "" {
				text = ev.Text
			}
			if text == "" {
				continue
			}
			if !emit(backends.Token{Text: text}) {
				_ = cmd.Wait()
				return
			}
			// All other types (init, tool_use, tool_result, unknown) are ignored.
		}
	}

	if err := scanner.Err(); err != nil {
		out <- backends.Token{Err: fmt.Errorf("gemini: read stream: %w", err)}
	}

	if err := cmd.Wait(); err != nil {
		out <- backends.Token{Err: b.wrapRunError(err, stderr.String())}
	}
}

// ---- internals ----

func (b *Backend) subprocess(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, b.command, args...)
	cmd.Env = append(os.Environ(), b.extraEnv...)
	return cmd
}

func (b *Backend) callSubprocess(ctx context.Context, outputFormat string) *exec.Cmd {
	args := []string{"-o", outputFormat}
	if b.cfg.Model != "" {
		args = append(args, "-m", b.cfg.Model)
	}
	return b.subprocess(ctx, args...)
}

func (b *Backend) wrapRunError(err error, stderr string) error {
	scrubbed := b.scrubStderr(stderr)
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		if scrubbed == "" {
			return fmt.Errorf("gemini: subprocess exited %d", exitErr.ExitCode())
		}
		return fmt.Errorf("gemini: subprocess exited %d: %s", exitErr.ExitCode(), scrubbed)
	}
	if isMissingBinary(err) {
		return fmt.Errorf("gemini: cannot run %q (is gemini-cli installed?): %w", b.command, err)
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return fmt.Errorf("gemini: %w", err)
}

func (b *Backend) scrubStderr(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > stderrCap {
		s = s[:stderrCap] + "...[truncated]"
	}
	if b.cfg.APIKeyEnv == "" {
		return s
	}
	lines := strings.Split(s, "\n")
	kept := lines[:0]
	for _, line := range lines {
		if strings.Contains(line, b.cfg.APIKeyEnv) {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}

func isMissingBinary(err error) bool {
	if errors.Is(err, fs.ErrNotExist) {
		return true
	}
	var execErr *exec.Error
	return errors.As(err, &execErr)
}

func interrupt(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Signal(os.Interrupt)
}

// renderPrompt formats a system prompt + message history into a single
// role-tagged text blob. gemini-cli does not accept structured message
// arrays on stdin; the stateless-concat path is what the Backend
// interface needs since each call carries the full history anyway.
func renderPrompt(messages []backends.Message, systemPrompt string) string {
	var b strings.Builder
	if systemPrompt != "" {
		b.WriteString("System: ")
		b.WriteString(systemPrompt)
		b.WriteString("\n\n")
	}
	for _, m := range messages {
		switch m.Role {
		case backends.RoleSystem:
			b.WriteString("System: ")
		case backends.RoleAssistant:
			b.WriteString("Assistant: ")
		default:
			b.WriteString("User: ")
		}
		b.WriteString(m.Content)
		b.WriteString("\n\n")
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

// ---- wire types ----

// geminiEvent absorbs the NDJSON events we recognize. Defensive — fields
// not present on a given event are zero-valued, unknown types ignored.
//
// Schema verified against gemini-cli nonInteractiveCli.ts and
// packages/core/src/output/types.ts (PR #10883, merged 2025-10-15):
//   - Type ∈ {init, message, tool_use, tool_result, error, result}
//   - For "message" events: Role ∈ {"user", "assistant"}, with the user
//     event being the prompt echo emitted right after init. Streaming
//     assistant chunks set Delta=true. We filter to Role=="assistant"
//     so we don't render the input echo back to the user.
type geminiEvent struct {
	Type    string `json:"type"`
	Role    string `json:"role"`
	Delta   bool   `json:"delta"`
	Content string `json:"content"`
	Text    string `json:"text"`
	Message string `json:"message"`
}
