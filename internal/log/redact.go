// Package log provides secret redaction for log lines, error strings,
// and any persisted/printed text (BUILD_ORDER Cross-cutting Security
// (c)). It scrubs; it never suppresses or drops a line.
package log

import (
	"context"
	"io"
	"log/slog"
	"os"
	"regexp"
	"strings"
)

const placeholder = "[REDACTED]"

var (
	secretEnvName = regexp.MustCompile(`(?i)(KEY|TOKEN|SECRET|PASSWORD)$`)
	// Provider key prefixes seen long-form (sk-..., gho_..., ghp_...).
	// {16,}: real provider tokens far exceed 16 post-prefix non-space/quote chars.
	providerKey = regexp.MustCompile(`\b(sk-|gho_|ghp_|ghs_|github_pat_)[^\s"']{16,}`)
)

// Redactor holds the literal secret values to scrub.
type Redactor struct{ secrets []string }

// NewRedactor snapshots the process env once: values of vars whose
// name ends KEY/TOKEN/SECRET/PASSWORD, non-empty, length >= 8.
func NewRedactor() *Redactor {
	r := &Redactor{}
	for _, kv := range os.Environ() {
		i := strings.IndexByte(kv, '=')
		if i < 0 {
			continue
		}
		name, val := kv[:i], kv[i+1:]
		if len(val) >= 8 && secretEnvName.MatchString(name) { // len >= 8: skip trivially-short values unlikely to be real secrets; avoids over-redacting common words
			r.secrets = append(r.secrets, val)
		}
	}
	return r
}

// Redact replaces every known secret value and provider-key pattern
// with [REDACTED]. Pure; empty redactor with no env match still scrubs
// provider-key patterns (defense in depth) but is otherwise a
// pass-through.
func (r *Redactor) Redact(s string) string {
	for _, sec := range r.secrets {
		if sec != "" {
			s = strings.ReplaceAll(s, sec, placeholder)
		}
	}
	return providerKey.ReplaceAllString(s, placeholder)
}

type redactWriter struct {
	r *Redactor
	w io.Writer
}

func (rw redactWriter) Write(p []byte) (int, error) {
	if _, err := io.WriteString(rw.w, rw.r.Redact(string(p))); err != nil {
		return 0, err // underlying write failed: report the real error. scrub-never-drop governs the SUCCESS path (return len(p)), not this one.
	}
	return len(p), nil // report original length: never signal a short write
}

// Writer wraps w so every write is redacted. Reports the original
// byte count so callers never see a short-write (scrub, never drop).
// Note: redaction is per-Write call — a secret split across two separate
// writes is NOT caught. The intended sinks (single fmt.Fprintln/error-string
// writes) are atomic, so this is safe for the M4c scope.
func (r *Redactor) Writer(w io.Writer) io.Writer { return redactWriter{r: r, w: w} }

type redactHandler struct {
	r *Redactor
	h slog.Handler
}

// Handler decorates h so the log message and TOP-LEVEL string attribute
// values are redacted before emit. Limitations (intentional — this is a
// forward-path decorator, not wired live in M4c since no slog sinks exist):
// group children (slog.KindGroup), non-string values (e.g. slog.Any
// wrapping an error whose text holds a secret), and LogValuer resolutions
// are NOT walked — see redactAttr. A future maintainer wiring slog must
// account for these before relying on this handler.
func (r *Redactor) Handler(h slog.Handler) slog.Handler { return redactHandler{r: r, h: h} }

func (rh redactHandler) Enabled(ctx context.Context, l slog.Level) bool { return rh.h.Enabled(ctx, l) }
func (rh redactHandler) WithGroup(name string) slog.Handler {
	return redactHandler{r: rh.r, h: rh.h.WithGroup(name)}
}
func (rh redactHandler) WithAttrs(as []slog.Attr) slog.Handler {
	return redactHandler{r: rh.r, h: rh.h.WithAttrs(rh.redactAttrs(as))}
}

func (rh redactHandler) Handle(ctx context.Context, rec slog.Record) error {
	nr := slog.NewRecord(rec.Time, rec.Level, rh.r.Redact(rec.Message), rec.PC)
	rec.Attrs(func(a slog.Attr) bool {
		nr.AddAttrs(rh.redactAttr(a))
		return true
	})
	return rh.h.Handle(ctx, nr)
}

func (rh redactHandler) redactAttrs(as []slog.Attr) []slog.Attr {
	out := make([]slog.Attr, len(as))
	for i, a := range as {
		out[i] = rh.redactAttr(a)
	}
	return out
}

func (rh redactHandler) redactAttr(a slog.Attr) slog.Attr {
	if a.Value.Kind() == slog.KindString {
		return slog.String(a.Key, rh.r.Redact(a.Value.String()))
	}
	return a
}
