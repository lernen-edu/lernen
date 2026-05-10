// Package openai implements the Backend interface against OpenAI's
// Chat Completions API at https://api.openai.com/v1.
//
// This is the "no CLI required, API key only" path for users with an
// OpenAI API key from platform.openai.com. Users on a ChatGPT Plus
// subscription who want their plan's bundled API credits to cover
// Lernen's calls should use the codex backend (which subprocesses the
// codex CLI and inherits its `codex login` auth state). The openai
// package does not implement the private "Sign in with ChatGPT" flow
// that the Codex CLI uses internally — that's not a documented OpenAI
// surface and we don't want to track it.
//
// Wire format mirrors openrouter (which is OpenAI-compatible). Diffs
// from openrouter:
//   - Base URL is https://api.openai.com/v1 (not openrouter.ai/api/v1)
//   - No HTTP-Referer or X-OpenRouter-Title analytics headers
//   - HealthCheck calls GET /models (lightweight, validates key)
//   - SSE stream contains no `: OPENROUTER PROCESSING` keep-alive
//     comments, but parseSSE remains tolerant of any non-`data:` line
//
// Retry policy mirrors openrouter: one retry on 5xx or 429 with a
// 500ms backoff; no retry on other 4xx (configuration errors the user
// needs to see).
package openai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/lernen-edu/lernen/internal/backends"
)

const (
	defaultBaseURL = "https://api.openai.com/v1"

	chatTimeout   = 120 * time.Second
	streamTimeout = 600 * time.Second
	retryBackoff  = 500 * time.Millisecond
)

// Compile-time check.
var _ backends.Backend = (*Backend)(nil)

// Config carries the OpenAI-specific settings.
type Config struct {
	// APIKeyEnv names the environment variable holding the OpenAI API key.
	APIKeyEnv string
	// Model is the OpenAI model identifier (e.g. "gpt-5.4").
	Model string
}

// Backend is the OpenAI HTTP backend.
type Backend struct {
	cfg     Config
	apiKey  string
	baseURL string
	client  *http.Client
}

// Option configures a Backend at construction time.
type Option func(*Backend)

// WithBaseURL overrides the OpenAI base URL. For tests against
// httptest.NewServer.
func WithBaseURL(u string) Option {
	return func(b *Backend) {
		b.baseURL = strings.TrimRight(u, "/")
	}
}

// WithHTTPClient overrides the http.Client. For tests with custom
// transports.
func WithHTTPClient(c *http.Client) Option {
	return func(b *Backend) { b.client = c }
}

// New constructs a Backend. The API key is resolved at construction time
// from the environment variable named in cfg.APIKeyEnv. An empty
// resolved value is not an error here — HealthCheck, Chat, and StreamChat
// surface a clear error when called.
func New(cfg Config, opts ...Option) *Backend {
	b := &Backend{
		cfg:     cfg,
		apiKey:  os.Getenv(cfg.APIKeyEnv),
		baseURL: defaultBaseURL,
		client:  &http.Client{},
	}
	for _, opt := range opts {
		opt(b)
	}
	return b
}

// Name returns "openai".
func (b *Backend) Name() string { return "openai" }

// HealthCheck issues GET /models. Returns nil on 2xx; a wrapped error
// otherwise. 401 is surfaced with a hint to check the configured
// API-key env var.
func (b *Backend) HealthCheck(ctx context.Context) error {
	if b.apiKey == "" {
		return b.missingKeyErr()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.baseURL+"/models", nil)
	if err != nil {
		return fmt.Errorf("openai: build request: %w", err)
	}
	b.setStandardHeaders(req)

	resp, err := b.client.Do(req)
	if err != nil {
		return fmt.Errorf("openai: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("openai: 401 Unauthorized — check the %s environment variable", b.cfg.APIKeyEnv)
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("openai: health check returned HTTP %d", resp.StatusCode)
	}
	return nil
}

// Chat performs a non-streaming completion.
func (b *Backend) Chat(ctx context.Context, messages []backends.Message, systemPrompt string) (backends.Response, error) {
	if b.apiKey == "" {
		return backends.Response{}, b.missingKeyErr()
	}

	reqCtx, cancel := context.WithTimeout(ctx, chatTimeout)
	defer cancel()

	payload, err := json.Marshal(chatRequest{
		Model:    b.cfg.Model,
		Messages: toWire(messages, systemPrompt),
		Stream:   false,
	})
	if err != nil {
		return backends.Response{}, fmt.Errorf("openai: marshal request: %w", err)
	}

	resp, err := b.postWithRetry(reqCtx, payload, false)
	if err != nil {
		return backends.Response{}, err
	}
	defer resp.Body.Close()

	var parsed chatResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return backends.Response{}, fmt.Errorf("openai: decode response: %w", err)
	}
	if len(parsed.Choices) == 0 {
		return backends.Response{}, errors.New("openai: response has no choices")
	}
	return backends.Response{Content: parsed.Choices[0].Message.Content}, nil
}

// StreamChat performs a streaming completion. The returned channel
// emits Tokens until [DONE] or end-of-stream; on ctx cancel, a single
// Token{Err: ctx.Err()} is emitted before the channel closes.
func (b *Backend) StreamChat(ctx context.Context, messages []backends.Message, systemPrompt string) (<-chan backends.Token, error) {
	if b.apiKey == "" {
		return nil, b.missingKeyErr()
	}

	payload, err := json.Marshal(chatRequest{
		Model:    b.cfg.Model,
		Messages: toWire(messages, systemPrompt),
		Stream:   true,
	})
	if err != nil {
		return nil, fmt.Errorf("openai: marshal request: %w", err)
	}

	resp, err := b.postWithRetry(ctx, payload, true)
	if err != nil {
		return nil, err
	}

	out := make(chan backends.Token, 16)
	go parseSSE(ctx, resp.Body, out)
	return out, nil
}

// ---- internal helpers ----

func (b *Backend) missingKeyErr() error {
	return fmt.Errorf("openai: %s environment variable is not set; configure it or run `lernen setup`", b.cfg.APIKeyEnv)
}

// setStandardHeaders applies the headers every OpenAI request uses.
// OpenAI does not require analytics headers like OpenRouter does.
func (b *Backend) setStandardHeaders(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+b.apiKey)
}

// postWithRetry POSTs payload to /chat/completions, retrying once on
// 5xx or 429. 4xx (excluding 429) is returned immediately. Network
// errors retry once. Returns the response with body still open; caller
// closes.
func (b *Backend) postWithRetry(ctx context.Context, payload []byte, streaming bool) (*http.Response, error) {
	const maxAttempts = 2
	var lastErr error

	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-time.After(retryBackoff):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.baseURL+"/chat/completions", bytes.NewReader(payload))
		if err != nil {
			return nil, fmt.Errorf("openai: build request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		if streaming {
			req.Header.Set("Accept", "text/event-stream")
		}
		b.setStandardHeaders(req)

		resp, err := b.client.Do(req)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil, err
			}
			lastErr = fmt.Errorf("openai: %w", err)
			continue
		}

		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 8*1024))
			resp.Body.Close()
			lastErr = fmt.Errorf("openai: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
			continue
		}

		if resp.StatusCode >= 400 {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 8*1024))
			resp.Body.Close()
			return nil, fmt.Errorf("openai: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		}

		return resp, nil
	}

	if lastErr == nil {
		lastErr = errors.New("openai: retry exhausted with no recorded error")
	}
	return nil, fmt.Errorf("openai: retry exhausted: %w", lastErr)
}

// parseSSE reads an SSE stream from body and forwards content tokens
// onto out. On [DONE], stream-end, or read error, closes out (and
// body). On ctx cancel, emits a single error token before closing.
func parseSSE(ctx context.Context, body io.ReadCloser, out chan<- backends.Token) {
	defer close(out)
	defer body.Close()

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 4096), 256*1024)

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			out <- backends.Token{Err: ctx.Err()}
			return
		default:
		}

		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			return
		}

		var chunk streamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		text := chunk.Choices[0].Delta.Content
		if text == "" {
			continue
		}
		select {
		case <-ctx.Done():
			out <- backends.Token{Err: ctx.Err()}
			return
		case out <- backends.Token{Text: text}:
		}
	}

	if err := scanner.Err(); err != nil {
		out <- backends.Token{Err: fmt.Errorf("openai: read stream: %w", err)}
	}
}

// streamTimeoutFor is unused but kept as a documented constant for
// callers that want to wrap StreamChat with a per-stream deadline.
// The Backend itself does not impose one because long tutor turns
// are legitimate.
var _ = streamTimeout

// ---- wire types ----

type chatRequest struct {
	Model    string        `json:"model"`
	Messages []wireMessage `json:"messages"`
	Stream   bool          `json:"stream"`
}

type wireMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatResponse struct {
	Choices []chatChoice `json:"choices"`
}

type chatChoice struct {
	Message wireMessage `json:"message"`
}

type streamChunk struct {
	Choices []streamChoice `json:"choices"`
}

type streamChoice struct {
	Delta wireMessage `json:"delta"`
}

func toWire(messages []backends.Message, systemPrompt string) []wireMessage {
	out := make([]wireMessage, 0, len(messages)+1)
	if systemPrompt != "" {
		out = append(out, wireMessage{Role: backends.RoleSystem, Content: systemPrompt})
	}
	for _, m := range messages {
		out = append(out, wireMessage{Role: m.Role, Content: m.Content})
	}
	return out
}
