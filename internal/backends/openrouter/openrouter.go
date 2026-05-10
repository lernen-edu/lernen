// Package openrouter implements the Backend interface against OpenRouter's
// HTTP API at https://openrouter.ai/api/v1.
//
// Wire format and behaviors verified against Context7's OpenRouter docs
// (see docs/PRE_BUILD_ANSWERS.md §4):
//   - Authorization: Bearer <key>
//   - Optional analytics headers: HTTP-Referer, X-OpenRouter-Title
//     (note: the title header is X-OpenRouter-Title, NOT X-Title)
//   - Streaming responses are SSE; the wire emits ": OPENROUTER PROCESSING"
//     comment payloads as keep-alives, which we skip
//   - HealthCheck uses GET /api/v1/key — free, validates the API key, and
//     surfaces credit/limit info we may want to display later
//
// Retry policy: one retry on 5xx or 429 with a 500 ms backoff; no retry on
// other 4xx (those are configuration errors the user needs to see). Retries
// honor context cancellation during the backoff.
package openrouter

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
	defaultBaseURL = "https://openrouter.ai/api/v1"
	defaultReferer = "https://github.com/lernen-edu/lernen"
	defaultTitle   = "Lernen"

	chatTimeout   = 120 * time.Second
	streamTimeout = 600 * time.Second
	retryBackoff  = 500 * time.Millisecond
)

// Compile-time check: *Backend implements backends.Backend.
var _ backends.Backend = (*Backend)(nil)

// Config carries the OpenRouter-specific settings the Backend needs.
// Decoupled from internal/config so backends do not learn about TOML.
type Config struct {
	// APIKeyEnv names the environment variable holding the API key.
	APIKeyEnv string
	// Model is the OpenRouter model identifier.
	Model string
}

// Backend is the OpenRouter HTTP backend.
type Backend struct {
	cfg     Config
	apiKey  string // resolved from cfg.APIKeyEnv at construction time
	baseURL string
	referer string
	title   string
	client  *http.Client
}

// Option configures a Backend at construction time.
type Option func(*Backend)

// WithBaseURL overrides the OpenRouter base URL. For tests against
// httptest.NewServer.
func WithBaseURL(u string) Option {
	return func(b *Backend) {
		b.baseURL = strings.TrimRight(u, "/")
	}
}

// WithHTTPClient overrides the http.Client. For tests with custom transports.
func WithHTTPClient(c *http.Client) Option {
	return func(b *Backend) { b.client = c }
}

// WithReferer overrides the HTTP-Referer header value.
func WithReferer(r string) Option {
	return func(b *Backend) { b.referer = r }
}

// WithTitle overrides the X-OpenRouter-Title header value.
func WithTitle(t string) Option {
	return func(b *Backend) { b.title = t }
}

// New constructs a Backend. The API key is resolved at construction time from
// the environment variable named in cfg.APIKeyEnv. An empty resolved value is
// not an error here — HealthCheck, Chat, and StreamChat will surface a clear
// error when called, which the caller can route to `lernen setup`.
func New(cfg Config, opts ...Option) *Backend {
	b := &Backend{
		cfg:     cfg,
		apiKey:  os.Getenv(cfg.APIKeyEnv),
		baseURL: defaultBaseURL,
		referer: defaultReferer,
		title:   defaultTitle,
		client:  &http.Client{},
	}
	for _, opt := range opts {
		opt(b)
	}
	return b
}

// Name returns "openrouter".
func (b *Backend) Name() string { return "openrouter" }

// HealthCheck issues GET /key. Returns nil on 2xx; a wrapped error otherwise.
// 401 is surfaced with a hint to check the configured API-key env var.
func (b *Backend) HealthCheck(ctx context.Context) error {
	if b.apiKey == "" {
		return b.missingKeyErr()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.baseURL+"/key", nil)
	if err != nil {
		return fmt.Errorf("openrouter: build request: %w", err)
	}
	b.setStandardHeaders(req)

	resp, err := b.client.Do(req)
	if err != nil {
		return fmt.Errorf("openrouter: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("openrouter: 401 Unauthorized — check the %s environment variable", b.cfg.APIKeyEnv)
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("openrouter: health check returned HTTP %d", resp.StatusCode)
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
		return backends.Response{}, fmt.Errorf("openrouter: marshal request: %w", err)
	}

	resp, err := b.postWithRetry(reqCtx, payload, false)
	if err != nil {
		return backends.Response{}, err
	}
	defer resp.Body.Close()

	var parsed chatResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return backends.Response{}, fmt.Errorf("openrouter: decode response: %w", err)
	}
	if len(parsed.Choices) == 0 {
		return backends.Response{}, errors.New("openrouter: response has no choices")
	}
	return backends.Response{Content: parsed.Choices[0].Message.Content}, nil
}

// StreamChat performs a streaming completion. The returned channel emits
// Tokens until [DONE] or end-of-stream; on ctx cancel, a single
// Token{Err: ctx.Err()} is emitted before the channel closes.
//
// SSE lines that don't start with "data: " (blank lines, OpenRouter's
// ": OPENROUTER PROCESSING" comment payloads) are silently skipped.
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
		return nil, fmt.Errorf("openrouter: marshal request: %w", err)
	}

	// Streaming uses the caller's context; no per-request timeout would let
	// a long-lived stream get killed mid-response.
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
	return fmt.Errorf("openrouter: %s environment variable is not set; configure it or run `lernen setup`", b.cfg.APIKeyEnv)
}

// setStandardHeaders applies the headers every OpenRouter request uses.
// The title header is X-OpenRouter-Title (not X-Title — the older docs lied).
func (b *Backend) setStandardHeaders(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+b.apiKey)
	if b.referer != "" {
		req.Header.Set("HTTP-Referer", b.referer)
	}
	if b.title != "" {
		req.Header.Set("X-OpenRouter-Title", b.title)
	}
}

// postWithRetry POSTs payload to /chat/completions, retrying once on 5xx or
// 429. 4xx (excluding 429) is returned immediately. Network errors retry once.
// Returns the response with body still open; caller closes.
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
			return nil, fmt.Errorf("openrouter: build request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		if streaming {
			req.Header.Set("Accept", "text/event-stream")
		}
		b.setStandardHeaders(req)

		resp, err := b.client.Do(req)
		if err != nil {
			// Don't retry context errors — propagate them.
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil, err
			}
			lastErr = fmt.Errorf("openrouter: %w", err)
			continue
		}

		// Retryable status codes: 429 and 5xx.
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 8*1024))
			resp.Body.Close()
			lastErr = fmt.Errorf("openrouter: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
			continue
		}

		// Non-retryable 4xx: surface immediately with body for context.
		if resp.StatusCode >= 400 {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 8*1024))
			resp.Body.Close()
			return nil, fmt.Errorf("openrouter: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		}

		// 2xx success.
		return resp, nil
	}

	if lastErr == nil {
		lastErr = errors.New("openrouter: retry exhausted with no recorded error")
	}
	return nil, fmt.Errorf("openrouter: retry exhausted: %w", lastErr)
}

// parseSSE reads an SSE stream from body and forwards content tokens onto out.
// On [DONE], stream-end, or read error, closes out (and body). On ctx cancel,
// emits a single error token before closing.
func parseSSE(ctx context.Context, body io.ReadCloser, out chan<- backends.Token) {
	defer close(out)
	defer body.Close()

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 4096), 256*1024)

	for scanner.Scan() {
		// Check ctx between lines so a slow stream still cancels promptly.
		select {
		case <-ctx.Done():
			out <- backends.Token{Err: ctx.Err()}
			return
		default:
		}

		line := scanner.Text()

		// Skip comment payloads (": OPENROUTER PROCESSING") and blank lines.
		// Anything that isn't a "data: " line is ignored per SSE conventions.
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			return
		}

		var chunk streamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			// Tolerant of unknown chunk shapes; OpenRouter occasionally emits
			// non-content events that we don't care about.
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
		out <- backends.Token{Err: fmt.Errorf("openrouter: read stream: %w", err)}
	}
}

// ---- wire types ----

// chatRequest is the body of POST /chat/completions.
type chatRequest struct {
	Model    string        `json:"model"`
	Messages []wireMessage `json:"messages"`
	Stream   bool          `json:"stream"`
}

type wireMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// chatResponse is the body of a non-streaming /chat/completions response.
type chatResponse struct {
	Choices []chatChoice `json:"choices"`
}

type chatChoice struct {
	Message wireMessage `json:"message"`
}

// streamChunk is one SSE data: payload during streaming.
type streamChunk struct {
	Choices []streamChoice `json:"choices"`
}

type streamChoice struct {
	Delta wireMessage `json:"delta"`
}

// toWire converts harness Messages to the OpenAI wire format, prepending the
// system prompt as a system-role message if non-empty.
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
