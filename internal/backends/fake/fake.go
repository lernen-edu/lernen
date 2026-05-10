// Package fake provides a Backend implementation for tests.
//
// FakeBackend returns canned responses in order, records the messages it was
// sent (so tests can assert on system-prompt + user-message construction),
// streams in configurable byte-sized chunks (to exercise streaming consumers),
// and honors context cancellation. It is the test substrate for every
// behavioral component that talks to a Backend in M1 — the Phase 1 firewall
// retry loop (step 13), the TUI streaming path (step 12), and the end-to-end
// smoke test (step 14).
package fake

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/lernen-edu/lernen/internal/backends"
)

// Compile-time check: *FakeBackend implements backends.Backend.
var _ backends.Backend = (*FakeBackend)(nil)

// FakeBackend is a controllable Backend for tests. Construct one with New;
// adjust streaming behavior with the Set* methods. Methods may be called
// concurrently; the canned-response queue is consumed under a mutex.
type FakeBackend struct {
	mu sync.Mutex

	name       string
	responses  []string
	healthErr  error
	chunkSize  int           // bytes per StreamChat token; 0 means whole response in one token
	chunkDelay time.Duration // delay between streamed tokens; 0 means no delay

	callIdx int
	sentLog [][]backends.Message
}

// New constructs a FakeBackend that will return responses in order from
// Chat() and StreamChat(). The default name is "fake"; the default chunk size
// for streaming is 8 bytes per token. With no responses, every Chat or
// StreamChat call returns an exhaustion error.
func New(responses ...string) *FakeBackend {
	return &FakeBackend{
		name:      "fake",
		responses: responses,
		chunkSize: 8,
	}
}

// SetName changes the backend's reported name. Default "fake".
func (b *FakeBackend) SetName(name string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.name = name
}

// SetHealthCheckError configures HealthCheck to return err on every call.
// Pass nil to restore the healthy default.
func (b *FakeBackend) SetHealthCheckError(err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.healthErr = err
}

// SetChunkSize configures the byte size of each StreamChat token. Zero means
// emit the entire canned response as a single token.
func (b *FakeBackend) SetChunkSize(n int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.chunkSize = n
}

// SetChunkDelay configures the delay between streamed tokens. Zero means no
// delay (tests usually want zero; integration scenarios may want a small
// delay to exercise per-token UI behavior).
func (b *FakeBackend) SetChunkDelay(d time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.chunkDelay = d
}

// Name reports the configured backend name.
func (b *FakeBackend) Name() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.name
}

// HealthCheck returns the configured health error (nil by default).
func (b *FakeBackend) HealthCheck(ctx context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	return b.healthErr
}

// Calls returns a snapshot of the message slices this backend has been sent,
// in call order. Each entry has the system prompt prepended as a Message with
// role=RoleSystem so tests can assert on the full prompt construction.
func (b *FakeBackend) Calls() [][]backends.Message {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([][]backends.Message, len(b.sentLog))
	for i, c := range b.sentLog {
		out[i] = append([]backends.Message(nil), c...)
	}
	return out
}

// Chat consumes the next canned response and returns it. Returns an error if
// the canned list has been exhausted or if ctx is already cancelled.
func (b *FakeBackend) Chat(ctx context.Context, messages []backends.Message, systemPrompt string) (backends.Response, error) {
	if err := ctx.Err(); err != nil {
		return backends.Response{}, err
	}

	b.mu.Lock()
	if b.callIdx >= len(b.responses) {
		idx := b.callIdx
		total := len(b.responses)
		b.mu.Unlock()
		return backends.Response{}, fmt.Errorf("FakeBackend: no canned response for call %d (configured %d responses)", idx, total)
	}
	resp := b.responses[b.callIdx]
	b.callIdx++
	b.recordCallLocked(messages, systemPrompt)
	b.mu.Unlock()

	return backends.Response{Content: resp}, nil
}

// StreamChat consumes the next canned response and streams it back as Tokens
// of size chunkSize bytes (or as a single token if chunkSize is 0 or larger
// than the response). Honors ctx cancellation: on cancel, the backend emits
// a single Token{Err: ctx.Err()} and closes the channel.
func (b *FakeBackend) StreamChat(ctx context.Context, messages []backends.Message, systemPrompt string) (<-chan backends.Token, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	b.mu.Lock()
	if b.callIdx >= len(b.responses) {
		idx := b.callIdx
		total := len(b.responses)
		b.mu.Unlock()
		return nil, fmt.Errorf("FakeBackend: no canned response for call %d (configured %d responses)", idx, total)
	}
	resp := b.responses[b.callIdx]
	b.callIdx++
	b.recordCallLocked(messages, systemPrompt)
	chunkSize := b.chunkSize
	chunkDelay := b.chunkDelay
	b.mu.Unlock()

	out := make(chan backends.Token, 16)
	go streamResponse(ctx, out, resp, chunkSize, chunkDelay)
	return out, nil
}

// streamResponse splits resp into chunks and sends each on out, honoring
// ctx and chunkDelay. Closes out before returning.
func streamResponse(ctx context.Context, out chan<- backends.Token, resp string, chunkSize int, chunkDelay time.Duration) {
	defer close(out)

	if chunkSize <= 0 || chunkSize >= len(resp) {
		select {
		case <-ctx.Done():
			out <- backends.Token{Err: ctx.Err()}
		case out <- backends.Token{Text: resp}:
		}
		return
	}

	for i := 0; i < len(resp); i += chunkSize {
		end := i + chunkSize
		if end > len(resp) {
			end = len(resp)
		}
		chunk := resp[i:end]

		if chunkDelay > 0 {
			t := time.NewTimer(chunkDelay)
			select {
			case <-ctx.Done():
				t.Stop()
				out <- backends.Token{Err: ctx.Err()}
				return
			case <-t.C:
			}
		}

		select {
		case <-ctx.Done():
			out <- backends.Token{Err: ctx.Err()}
			return
		case out <- backends.Token{Text: chunk}:
		}
	}
}

// recordCallLocked appends the given messages (with systemPrompt prepended
// as a system Message) to sentLog. Must be called with b.mu held.
func (b *FakeBackend) recordCallLocked(messages []backends.Message, systemPrompt string) {
	full := make([]backends.Message, 0, len(messages)+1)
	full = append(full, backends.Message{Role: backends.RoleSystem, Content: systemPrompt})
	full = append(full, messages...)
	b.sentLog = append(b.sentLog, full)
}
