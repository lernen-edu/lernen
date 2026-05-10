// Package google implements the Backend interface against Google's
// Gemini Generative Language API at
// https://generativelanguage.googleapis.com/v1beta.
//
// This is the "no CLI required, API key only" path for users with a
// Gemini API key from aistudio.google.com. Users who want to lean on
// gemini-cli's own auth (free Cloud Code Assist tier, Vertex ADC,
// etc.) keep using the gemini subprocess backend, which delegates to
// the gemini binary's own credentials.
//
// Google's Gemini API is NOT OpenAI-compatible. Notable differences
// from the openai package:
//
//   - Auth header is `x-goog-api-key: $KEY` (not Authorization: Bearer).
//   - Endpoint path embeds the model id and the operation:
//     `/v1beta/models/<model>:generateContent` (or :streamGenerateContent
//     with ?alt=sse for SSE streaming).
//   - Request shape is `{systemInstruction:{parts:[{text}]},
//     contents:[{role, parts:[{text}]}]}` — system content is top-level,
//     not a wireMessage with role:"system".
//   - Assistant role label is "model" (Google's term) — Lernen's
//     RoleAssistant is mapped to that.
//   - Response: `candidates[0].content.parts[0].text` carries the
//     generated text. SSE chunks have the same shape; there is no
//     [DONE] sentinel, the stream just ends.
//
// Wire format verified via Context7 against ai.google.dev docs.
//
// Retry policy mirrors openai/openrouter: one retry on 5xx or 429 with
// 500ms backoff; no retry on other 4xx.
package google

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/lernen-edu/lernen/internal/backends"
)

const (
	defaultBaseURL = "https://generativelanguage.googleapis.com/v1beta"

	chatTimeout  = 120 * time.Second
	retryBackoff = 500 * time.Millisecond
)

// Compile-time check.
var _ backends.Backend = (*Backend)(nil)

// Config carries the Google-specific settings.
type Config struct {
	// APIKeyEnv names the environment variable holding the Gemini API key
	// (e.g. "GEMINI_API_KEY").
	APIKeyEnv string
	// Model is the Gemini model identifier (e.g., "gemini-2.5-flash").
	Model string
}

// Backend is the Google Gemini HTTP backend.
type Backend struct {
	cfg     Config
	apiKey  string
	baseURL string
	client  *http.Client
}

// Option configures a Backend at construction time.
type Option func(*Backend)

// WithBaseURL overrides the Gemini base URL. For tests against
// httptest.NewServer.
func WithBaseURL(u string) Option {
	return func(b *Backend) { b.baseURL = strings.TrimRight(u, "/") }
}

// WithHTTPClient overrides the http.Client.
func WithHTTPClient(c *http.Client) Option {
	return func(b *Backend) { b.client = c }
}

// New constructs a Backend.
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

// Name returns "google".
func (b *Backend) Name() string { return "google" }

// HealthCheck issues GET /models. Returns nil on 2xx; a wrapped error
// otherwise. 401 (or 403, which Google returns for unauthorized API
// keys) is surfaced with a hint to check the env var.
func (b *Backend) HealthCheck(ctx context.Context) error {
	if b.apiKey == "" {
		return b.missingKeyErr()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.baseURL+"/models", nil)
	if err != nil {
		return fmt.Errorf("google: build request: %w", err)
	}
	b.setStandardHeaders(req)

	resp, err := b.client.Do(req)
	if err != nil {
		return fmt.Errorf("google: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("google: %d %s — check the %s environment variable", resp.StatusCode, http.StatusText(resp.StatusCode), b.cfg.APIKeyEnv)
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("google: health check returned HTTP %d", resp.StatusCode)
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

	payload, err := json.Marshal(toGeminiRequest(messages, systemPrompt))
	if err != nil {
		return backends.Response{}, fmt.Errorf("google: marshal request: %w", err)
	}

	resp, err := b.postWithRetry(reqCtx, "generateContent", payload, false)
	if err != nil {
		return backends.Response{}, err
	}
	defer resp.Body.Close()

	var parsed genResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return backends.Response{}, fmt.Errorf("google: decode response: %w", err)
	}
	text := parsed.firstText()
	if text == "" {
		return backends.Response{}, errors.New("google: response has no candidate text")
	}
	return backends.Response{Content: text}, nil
}

// StreamChat performs a streaming completion via :streamGenerateContent
// with ?alt=sse. The returned channel emits Tokens as SSE chunks
// arrive; on stream end or ctx cancel, the channel closes (with a
// terminal Token{Err: ctx.Err()} on the cancel path).
func (b *Backend) StreamChat(ctx context.Context, messages []backends.Message, systemPrompt string) (<-chan backends.Token, error) {
	if b.apiKey == "" {
		return nil, b.missingKeyErr()
	}

	payload, err := json.Marshal(toGeminiRequest(messages, systemPrompt))
	if err != nil {
		return nil, fmt.Errorf("google: marshal request: %w", err)
	}

	resp, err := b.postWithRetry(ctx, "streamGenerateContent", payload, true)
	if err != nil {
		return nil, err
	}

	out := make(chan backends.Token, 16)
	go parseSSE(ctx, resp.Body, out)
	return out, nil
}

// ---- internal helpers ----

func (b *Backend) missingKeyErr() error {
	return fmt.Errorf("google: %s environment variable is not set; configure it or run `lernen setup`", b.cfg.APIKeyEnv)
}

// setStandardHeaders applies the headers every Gemini request uses.
// Google requires only the API-key header; no Authorization, no
// analytics chrome.
func (b *Backend) setStandardHeaders(req *http.Request) {
	req.Header.Set("x-goog-api-key", b.apiKey)
}

// postWithRetry POSTs to the model+operation endpoint, retrying once
// on 5xx or 429. operation is "generateContent" or "streamGenerateContent".
func (b *Backend) postWithRetry(ctx context.Context, operation string, payload []byte, streaming bool) (*http.Response, error) {
	const maxAttempts = 2
	endpoint := b.endpointURL(operation, streaming)

	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-time.After(retryBackoff):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
		if err != nil {
			return nil, fmt.Errorf("google: build request: %w", err)
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
			lastErr = fmt.Errorf("google: %w", err)
			continue
		}

		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 8*1024))
			resp.Body.Close()
			lastErr = fmt.Errorf("google: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
			continue
		}

		if resp.StatusCode >= 400 {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 8*1024))
			resp.Body.Close()
			return nil, fmt.Errorf("google: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		}

		return resp, nil
	}

	if lastErr == nil {
		lastErr = errors.New("google: retry exhausted with no recorded error")
	}
	return nil, fmt.Errorf("google: retry exhausted: %w", lastErr)
}

// endpointURL builds the per-model URL Google requires:
//
//	<base>/models/<model>:<operation>           (non-streaming)
//	<base>/models/<model>:<operation>?alt=sse   (streaming)
//
// The model id is path-escaped in case it ever contains characters
// that aren't already URL-safe.
func (b *Backend) endpointURL(operation string, streaming bool) string {
	model := url.PathEscape(b.cfg.Model)
	endpoint := fmt.Sprintf("%s/models/%s:%s", b.baseURL, model, operation)
	if streaming {
		endpoint += "?alt=sse"
	}
	return endpoint
}

// parseSSE reads the SSE stream from body and forwards content tokens
// onto out. Each `data: <json>` line carries a genResponse-shaped chunk
// with `candidates[0].content.parts[0].text` containing the next slice
// of generated text. There is no [DONE] sentinel; the stream simply
// ends. On ctx cancel, emits Token{Err: ctx.Err()} before closing.
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
		if data == "" {
			continue
		}

		var chunk genResponse
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue // tolerant of unknown shapes
		}
		text := chunk.firstText()
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
		out <- backends.Token{Err: fmt.Errorf("google: read stream: %w", err)}
	}
}

// ---- request translation ----

// toGeminiRequest converts Lernen's []Message + systemPrompt into the
// Gemini request body. systemPrompt becomes the top-level
// systemInstruction (omitted entirely when empty). RoleAssistant maps
// to "model"; mid-conversation system messages are filtered out (the
// API accepts system content only at the top level).
func toGeminiRequest(messages []backends.Message, systemPrompt string) genRequest {
	req := genRequest{
		Contents: make([]genContent, 0, len(messages)),
	}
	if systemPrompt != "" {
		req.SystemInstruction = &genContent{
			Parts: []genPart{{Text: systemPrompt}},
		}
	}
	for _, m := range messages {
		role := ""
		switch m.Role {
		case backends.RoleAssistant:
			role = "model"
		case backends.RoleUser:
			role = "user"
		default:
			// RoleSystem mid-conversation is rare; Gemini doesn't accept
			// it inside contents. Skip it; callers should pass system
			// content via systemPrompt.
			continue
		}
		req.Contents = append(req.Contents, genContent{
			Role:  role,
			Parts: []genPart{{Text: m.Content}},
		})
	}
	return req
}

// ---- wire types ----

type genRequest struct {
	SystemInstruction *genContent  `json:"systemInstruction,omitempty"`
	Contents          []genContent `json:"contents"`
}

type genContent struct {
	Role  string    `json:"role,omitempty"`
	Parts []genPart `json:"parts"`
}

type genPart struct {
	Text string `json:"text"`
}

// genResponse covers both the non-streaming response shape and a single
// SSE chunk shape — they're identical per Google's docs (each stream
// chunk is a partial GenerateContentResponse).
type genResponse struct {
	Candidates []genCandidate `json:"candidates"`
}

type genCandidate struct {
	Content genContent `json:"content"`
}

// firstText returns the first non-empty text part from the first
// candidate, or "" if no text is present. Defensive against missing
// candidates / parts.
func (r genResponse) firstText() string {
	if len(r.Candidates) == 0 {
		return ""
	}
	for _, p := range r.Candidates[0].Content.Parts {
		if p.Text != "" {
			return p.Text
		}
	}
	return ""
}
