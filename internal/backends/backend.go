// Package backends defines the Backend interface that the lernen harness uses
// to talk to inference providers, plus the message/response/token value types
// shared across implementations.
//
// See docs/PRD.md §4.1 for the abstraction's purpose and docs/PRE_BUILD_ANSWERS.md
// §0 (owner decision b) for the Token shape used by streaming.
//
// One backend is active per process. The harness never speaks to a model
// directly; it speaks to a Backend.
package backends

import "context"

// Role values for chat messages. Matches the OpenAI conversation schema that
// every supported v0 backend implements directly or proxies to.
const (
	RoleSystem    = "system"
	RoleUser      = "user"
	RoleAssistant = "assistant"
)

// Message is one entry in a chat conversation history.
type Message struct {
	// Role is one of RoleSystem, RoleUser, RoleAssistant.
	Role string
	// Content is the textual body. Multimodal content is out of scope for v0.
	Content string
}

// Response is the result of a non-streaming Chat call. Deliberately minimal
// for v0; usage and finish-reason fields can be added when a backend needs
// to surface them.
type Response struct {
	Content string
}

// Token is one chunk delivered over a StreamChat channel. Per owner decision
// (b) in PRE_BUILD_ANSWERS §0, errors travel on the same channel as content
// — Err non-nil signals a terminal error and the channel will be closed
// immediately after.
type Token struct {
	Text string
	Err  error
}

// Backend is the contract every inference backend implements. Method
// semantics:
//
//   - Chat performs a non-streaming completion. Returns the full response
//     text or a terminal error.
//   - StreamChat performs a streaming completion. The returned channel emits
//     Tokens until either (a) it is closed by the backend after the last
//     content token, or (b) ctx is cancelled, in which case the backend
//     emits a single Token with Err = ctx.Err() and then closes the channel.
//   - HealthCheck performs a cheap probe (auth, network, binary present)
//     suitable for `lernen setup`. Returns nil if the backend is usable.
//   - Name returns a stable lowercase identifier ("openrouter", "codex",
//     "gemini", "fake") suitable for log fields and config validation.
//
// All methods are safe for concurrent use unless an implementation
// explicitly documents otherwise.
type Backend interface {
	Chat(ctx context.Context, messages []Message, systemPrompt string) (Response, error)
	StreamChat(ctx context.Context, messages []Message, systemPrompt string) (<-chan Token, error)
	HealthCheck(ctx context.Context) error
	Name() string
}
