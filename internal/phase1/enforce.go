// Package phase1 enforces the Phase 1 "no code longer than 3 lines" firewall
// described in PRD §4.1 and detailed in docs/PRE_BUILD_ANSWERS.md §2.
//
// Two entry points share one state machine: Filter (this file) for callers
// that already have the complete response, and Streamer (streamer.go) for
// the TUI which receives bytes incrementally. Filter is a thin shim over
// Streamer so both paths produce identical output and identical violations.
//
// The promise being enforced: the learner never sees code longer than three
// lines from the AI in Phase 1. That includes brief flickers during
// streaming — the Streamer holds code-block bodies until the boundary is
// known so the TUI can never paint an unverified body.
package phase1

import (
	"regexp"
	"strings"
)

// MaxCodeLines is the largest code block the tutor may produce in Phase 1.
// Code blocks with strictly more than this many body lines are replaced with
// FirewallMarker.
const MaxCodeLines = 3

// FirewallMarker replaces forbidden code-block bodies in filtered output.
// The literal text is stable so callers (TUI, logs, tests) can match against it.
const FirewallMarker = "[code block removed by Phase 1 firewall]"

// ViolationKind classifies why a code block was flagged.
type ViolationKind int

const (
	// KindFence is a fenced code block (```...```) longer than MaxCodeLines.
	KindFence ViolationKind = iota
	// KindIndented is a CommonMark indented code block (4+ space or tab
	// indented run preceded by a blank line) longer than MaxCodeLines.
	KindIndented
	// KindUnterminatedFence is a fence opener that never received a closing
	// fence before end of input. Always recorded so callers can WARN-log;
	// only stripped when the body is itself longer than MaxCodeLines.
	KindUnterminatedFence
)

// String returns the kind's stable lowercase name (suitable for log fields).
func (k ViolationKind) String() string {
	switch k {
	case KindFence:
		return "fence"
	case KindIndented:
		return "indented"
	case KindUnterminatedFence:
		return "unterminated-fence"
	default:
		return "unknown"
	}
}

// Violation describes one code block the firewall noticed. Stripped is true
// when the block's body was replaced with FirewallMarker; false when the body
// was preserved (the only such case in v0 is a short unterminated fence,
// recorded so the caller can still log it at WARN).
type Violation struct {
	Kind      ViolationKind
	BodyLines int
	StartLine int  // 0-indexed line in the original input where the block begins
	Stripped  bool // true if FirewallMarker replaced the body
}

// Filter scans a complete response and returns the filtered text plus a
// list of every code-block violation that was noticed. If the response
// contains no forbidden blocks, the returned text equals s and the slice
// is nil.
//
// Filter is now a thin shim over Streamer so the offline pre-display
// path and the streaming TUI path share one state machine. Tests in this
// package exercise Filter for the canonical 12+ scenarios; tests in
// streamer_test.go exercise the same scenarios fed byte-by-byte (the
// TestStreamer_WriteEqualsFilter property test asserts the two paths
// produce identical output and identical violations).
func Filter(s string) (string, []Violation) {
	if s == "" {
		return "", nil
	}
	str := NewStreamer()
	out, viols := str.Write([]byte(s))
	flushed, fviols := str.Flush()
	if len(fviols) > 0 {
		viols = append(viols, fviols...)
	}
	return out + flushed, viols
}

// ---- internal helpers ----

type lineState int

const (
	stateOutside lineState = iota
	stateInFence
	stateInIndentBlock
	stateDiscardingIndent
)

// fenceLineRe matches a complete-line code fence: three backticks optionally
// followed by an info string of word characters / + / - and trailing whitespace.
var fenceLineRe = regexp.MustCompile(`^` + "```" + `[A-Za-z0-9_+\-]*[ \t]*$`)

// isFenceLine reports whether the line is a full-line ``` opener or closer.
// Trailing whitespace is tolerated; no other content is allowed on the line.
func isFenceLine(s string) bool {
	return fenceLineRe.MatchString(s)
}

// isBlank reports whether the line is empty or whitespace-only.
func isBlank(s string) bool {
	for _, r := range s {
		if r != ' ' && r != '\t' {
			return false
		}
	}
	return true
}

// isIndented reports whether the line begins with at least 4 spaces or a tab.
// Whitespace-only lines are not considered indented (they are blank).
func isIndented(s string) bool {
	if isBlank(s) {
		return false
	}
	if strings.HasPrefix(s, "\t") {
		return true
	}
	if len(s) >= 4 && s[:4] == "    " {
		return true
	}
	return false
}

// lineRecord pairs a line's text with whether the original input had a
// trailing newline after it. The Streamer keeps this so reconstructed
// output matches the input's exact newline behavior — a response that
// ends without a final '\n' should remain that way after filtering.
type lineRecord struct {
	text string
	nl   bool
}
