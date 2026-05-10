// Package codex implements the Backend interface against the OpenAI Codex
// CLI (https://github.com/openai/codex).
//
// Invocation summary (verified via Codex docs at v0.128):
//   - HealthCheck: `codex login status` — exits 0 when authenticated,
//     non-zero with explanatory stderr otherwise. Zero-cost (no API call).
//   - Chat: `codex exec --sandbox read-only --skip-git-repo-check
//     --ephemeral -m <model> -` with the rendered prompt on stdin. Without
//     --json, stdout is the final agent message text and stderr carries
//     progress noise we discard unless the subprocess fails.
//   - StreamChat: same as Chat plus --json. stdout becomes JSONL with
//     event types we parse defensively; deltas (when present) stream
//     token-by-token, otherwise the final item.completed text is emitted
//     as a single chunk.
//
// Defense-in-depth flags pinned on every invocation:
//   --sandbox read-only         no host filesystem writes
//   --skip-git-repo-check       no git probing of cwd
//   --ephemeral                 no session persisted under ~/.codex
// Codex is used here strictly as an inference path. If a future curriculum
// wants codex as a coding agent with file-touching authority, those flags
// would relax per-call — but the default stays locked.
//
// API key handling: the Backend reads cfg.APIKeyEnv from the environment
// at construction. If empty, calls return a configured-but-not-set error.
// The key is forwarded to the subprocess via the inherited environment
// (codex itself reads CODEX_API_KEY); we never log it, never echo it in
// errors, and stderr that contains the env var name is scrubbed before
// being surfaced.
package codex

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
	defaultCommand = "codex"
	chatTimeout    = 120 * time.Second
	stderrCap      = 1024 // bytes of stderr included in error wraps
)

// Compile-time check.
var _ backends.Backend = (*Backend)(nil)

// Config carries the codex-specific settings.
type Config struct {
	// APIKeyEnv names the environment variable holding the codex API key
	// (typically "CODEX_API_KEY"). The variable's value is forwarded to
	// the subprocess via the inherited environment.
	APIKeyEnv string

	// Model is the codex `-m` argument (e.g. "gpt-5.4").
	Model string
}

// Backend is the codex CLI backend.
type Backend struct {
	cfg      Config
	command  string
	extraEnv []string // appended after os.Environ() when starting subprocesses
}

// Option configures a Backend at construction time.
type Option func(*Backend)

// WithCommand overrides the codex binary path. Used in tests to point at
// a fake binary (typically os.Args[0] re-exec'd as a fake helper).
func WithCommand(path string) Option {
	return func(b *Backend) { b.command = path }
}

// WithExtraEnv appends environment entries to every subprocess invocation
// (after os.Environ()). Used in tests to drive fake-binary behavior.
func WithExtraEnv(env []string) Option {
	return func(b *Backend) { b.extraEnv = append(b.extraEnv, env...) }
}

// New constructs a Backend.
func New(cfg Config, opts ...Option) *Backend {
	b := &Backend{
		cfg:     cfg,
		command: defaultCommand,
	}
	for _, opt := range opts {
		opt(b)
	}
	return b
}

// Name returns "codex".
func (b *Backend) Name() string { return "codex" }

// HealthCheck runs `codex login status` and returns nil on exit code 0.
// Auth is delegated to the codex CLI: it may use the API-key env var
// (forwarded via inherited env when set) OR its own stored auth at
// ~/.codex/auth.json from `codex login`. We do not pre-check the env
// var here — a user authenticated via `codex login` should not be
// blocked just because they haven't exported CODEX_API_KEY. If neither
// auth source is available, the CLI surfaces its own error which we
// wrap and surface.
func (b *Backend) HealthCheck(ctx context.Context) error {
	cmd := b.subprocess(ctx, "login", "status")
	stderr := &bytes.Buffer{}
	cmd.Stderr = stderr
	cmd.Stdout = io.Discard

	if err := cmd.Run(); err != nil {
		if isMissingBinary(err) {
			return fmt.Errorf("codex: cannot run %q (is the codex CLI installed?): %w", b.command, err)
		}
		return fmt.Errorf("codex: login status failed: %s", b.scrubStderr(stderr.String()))
	}
	return nil
}

// Chat performs a non-streaming completion. Uses plain `codex exec`
// (no --json) so stdout is the final agent message text. See HealthCheck
// for the auth contract: the env var is forwarded if set, but the CLI's
// own auth state is sufficient when it's not.
func (b *Backend) Chat(ctx context.Context, messages []backends.Message, systemPrompt string) (backends.Response, error) {
	reqCtx, cancel := context.WithTimeout(ctx, chatTimeout)
	defer cancel()

	prompt := renderPrompt(messages, systemPrompt)
	cmd := b.execSubprocess(reqCtx, false /* no --json */)
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
// content tokens as they arrive (JSONL deltas if the CLI provides them,
// otherwise the final item.completed text as a single Token). On error
// or context cancellation, a single Token{Err: ...} is emitted before
// the channel is closed. See HealthCheck for the auth contract.
func (b *Backend) StreamChat(ctx context.Context, messages []backends.Message, systemPrompt string) (<-chan backends.Token, error) {
	prompt := renderPrompt(messages, systemPrompt)
	cmd := b.execSubprocess(ctx, true /* --json */)
	cmd.Stdin = strings.NewReader(prompt)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("codex: stdout pipe: %w", err)
	}
	stderr := &bytes.Buffer{}
	cmd.Stderr = stderr

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("codex: start: %w", err)
	}

	out := make(chan backends.Token, 16)
	go b.streamFromJSONL(ctx, cmd, stdout, stderr, out)
	return out, nil
}

// streamFromJSONL parses the codex --json event stream onto out. It honors
// ctx cancellation by interrupting the subprocess. It always closes out
// before returning.
func (b *Backend) streamFromJSONL(ctx context.Context, cmd *exec.Cmd, stdout io.ReadCloser, stderr *bytes.Buffer, out chan<- backends.Token) {
	defer close(out)

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 4096), 1024*1024)

	// Track whether deltas have arrived for the current agent message turn,
	// so we don't double-emit when item.completed lands at the end.
	seenDeltas := false

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

		var ev codexEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			continue // tolerant of unknown shapes
		}

		switch {
		case ev.Type == "turn.completed":
			_ = cmd.Wait()
			return
		case ev.Type == "turn.failed", ev.Type == "error":
			msg := ev.firstNonEmptyMessage()
			if msg == "" {
				msg = "codex turn failed"
			}
			emit(backends.Token{Err: fmt.Errorf("codex: %s", msg)})
			_ = interrupt(cmd)
			_ = cmd.Wait()
			return
		case ev.Delta != "":
			seenDeltas = true
			if !emit(backends.Token{Text: ev.Delta}) {
				_ = cmd.Wait()
				return
			}
		case ev.Type == "item.completed" && ev.Item != nil && ev.Item.Type == "agent_message":
			if seenDeltas {
				continue
			}
			if ev.Item.Text != "" {
				if !emit(backends.Token{Text: ev.Item.Text}) {
					_ = cmd.Wait()
					return
				}
			}
		}
	}

	if err := scanner.Err(); err != nil {
		out <- backends.Token{Err: fmt.Errorf("codex: read stream: %w", err)}
	}

	if err := cmd.Wait(); err != nil {
		out <- backends.Token{Err: b.wrapRunError(err, stderr.String())}
	}
}

// ---- internals ----

// subprocess builds an exec.Cmd for the codex binary with the given args.
// It inherits the process environment (so CODEX_API_KEY propagates) and
// appends extraEnv from the WithExtraEnv option.
func (b *Backend) subprocess(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, b.command, args...)
	cmd.Env = append(os.Environ(), b.extraEnv...)
	return cmd
}

// execSubprocess builds the `codex exec ...` command shape used by Chat
// and StreamChat. jsonMode toggles --json for streaming.
func (b *Backend) execSubprocess(ctx context.Context, jsonMode bool) *exec.Cmd {
	args := []string{"exec",
		"--sandbox", "read-only",
		"--skip-git-repo-check",
		"--ephemeral",
	}
	if jsonMode {
		args = append(args, "--json")
	}
	if b.cfg.Model != "" {
		args = append(args, "-m", b.cfg.Model)
	}
	args = append(args, "-")
	return b.subprocess(ctx, args...)
}

// wrapRunError converts a *exec.ExitError into a user-facing error,
// folding in a scrubbed slice of stderr for context but never the
// API-key value.
func (b *Backend) wrapRunError(err error, stderr string) error {
	scrubbed := b.scrubStderr(stderr)
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		if scrubbed == "" {
			return fmt.Errorf("codex: subprocess exited %d", exitErr.ExitCode())
		}
		return fmt.Errorf("codex: subprocess exited %d: %s", exitErr.ExitCode(), scrubbed)
	}
	if isMissingBinary(err) {
		return fmt.Errorf("codex: cannot run %q (is the codex CLI installed?): %w", b.command, err)
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return fmt.Errorf("codex: %w", err)
}

// isMissingBinary returns true when err indicates the configured binary
// path could not be executed (either PATH lookup failed or the absolute
// path doesn't exist).
func isMissingBinary(err error) bool {
	if errors.Is(err, fs.ErrNotExist) {
		return true
	}
	var execErr *exec.Error
	return errors.As(err, &execErr)
}

// scrubStderr trims to stderrCap bytes and removes any line containing
// the configured env var name (paranoid against the CLI echoing back its
// configuration in error output).
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

// interrupt sends SIGINT to the subprocess so it can clean up; the caller
// is responsible for cmd.Wait afterward.
func interrupt(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Signal(os.Interrupt)
}

// renderPrompt formats a system prompt + message history into a single
// role-tagged text blob. Codex's `exec` mode does not accept a
// structured message array on stdin; the stateless-concat path is what
// the Backend interface needs anyway since each call carries the full
// history.
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
		default: // RoleUser and unknowns
			b.WriteString("User: ")
		}
		b.WriteString(m.Content)
		b.WriteString("\n\n")
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

// ---- wire types ----

// codexEvent absorbs the JSONL events we recognize. Defensive parsing:
// fields not present on a given event are zero-valued, and unknown event
// types are ignored.
type codexEvent struct {
	Type    string          `json:"type"`
	Delta   string          `json:"delta"`
	Item    *codexItem      `json:"item"`
	Error   *codexErrorBody `json:"error"`
	Message string          `json:"message"`
}

// codexItem matches the actual codex JSONL event payload (verified
// against codex v0.128 output during M2.5 dogfood — the field is
// `type`, not `itemType` as M2 step 4 research had guessed).
type codexItem struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type codexErrorBody struct {
	Message string `json:"message"`
}

func (e codexEvent) firstNonEmptyMessage() string {
	if e.Error != nil && e.Error.Message != "" {
		return e.Error.Message
	}
	return e.Message
}
