package phase1

import (
	"bytes"
	"strings"
)

// Streamer enforces the Phase 1 firewall on a streaming response (PRD §4.1
// and PRE_BUILD_ANSWERS §3). Bytes flow in via Write; "safe-to-display"
// bytes flow out as the return value. The body of an open code block is
// held until the block is confirmed safe — fenced blocks emit when the
// closer arrives, indented blocks emit when an outdent arrives. This is
// the no-flicker invariant the TUI depends on.
//
// Streamer is the same state machine as Filter, fed byte-by-byte instead
// of line-by-line. Filter is now a thin shim around Streamer.
type Streamer struct {
	state lineState

	// partialLine holds bytes since the last '\n'. On the next '\n' (or
	// on Flush) the accumulated bytes become a complete line and run
	// through processLine.
	partialLine []byte

	// lineIndex is the 0-indexed counter for input lines. Captured into
	// Violation.StartLine when a fence/indent block opens.
	lineIndex int

	// fence-state buffers
	fenceOpener    lineRecord
	fenceStartLine int
	fencePending   []lineRecord

	// indented-block buffers
	indentStartLine int
	indentPending   []lineRecord

	// lastEmittedBlank tracks whether the previous *emitted* line was
	// blank. Indented-block detection requires the prior line to be
	// blank (or the start of input).
	lastEmittedBlank bool

	// passthrough, when true, makes Write emit the input bytes verbatim
	// and Flush a no-op — the firewall is bypassed entirely. This is for
	// non-tutoring contexts (the forge's own mentor dialogue, per M3a/b/c
	// spec §2: "the firewall runs on forge outputs, not on the forge's
	// own dialogue"). Constructed via NewPassthroughStreamer.
	passthrough bool
}

// NewStreamer returns a Streamer ready to accept bytes. The "previous
// line" is treated as blank, matching Filter's input-start behavior so
// an indented block at offset 0 still triggers detection.
func NewStreamer() *Streamer {
	return &Streamer{
		state:            stateOutside,
		lastEmittedBlank: true,
	}
}

// NewPassthroughStreamer returns a Streamer that emits input bytes
// verbatim, never truncates code blocks, never records violations, and
// has no buffering on Flush. Used in non-tutoring contexts (forge
// stages) where the firewall's invariant — "the tutor never solves
// problems for the student by writing code" — does not apply. The
// returned value satisfies the same *Streamer type so the TUI's
// streamer wiring is unchanged.
func NewPassthroughStreamer() *Streamer {
	return &Streamer{passthrough: true}
}

// Write feeds bytes from the model's response stream. Returns the
// safe-to-display suffix produced by these bytes plus any violations
// recorded while processing them. The returned suffix may be shorter
// than the input — bytes inside an open code block are held until the
// block resolves.
func (s *Streamer) Write(b []byte) (string, []Violation) {
	if len(b) == 0 {
		return "", nil
	}
	if s.passthrough {
		return string(b), nil
	}
	var out strings.Builder
	var violations []Violation

	s.partialLine = append(s.partialLine, b...)
	for {
		i := bytes.IndexByte(s.partialLine, '\n')
		if i < 0 {
			break
		}
		line := lineRecord{text: string(s.partialLine[:i]), nl: true}
		s.partialLine = s.partialLine[i+1:]
		s.processLine(line, &out, &violations)
	}
	return out.String(), violations
}

// Flush handles end-of-stream. Any pending bytes since the last newline
// become a final line with nl=false (matching Filter's splitLines
// behavior). Then any unterminated state — open fence, open indent
// run — is resolved per the rules in §3 / §4.1.
func (s *Streamer) Flush() (string, []Violation) {
	if s.passthrough {
		return "", nil
	}
	var out strings.Builder
	var violations []Violation

	if len(s.partialLine) > 0 {
		line := lineRecord{text: string(s.partialLine), nl: false}
		s.partialLine = nil
		s.processLine(line, &out, &violations)
	}

	switch s.state {
	case stateInFence:
		bodyLines := len(s.fencePending)
		if bodyLines > MaxCodeLines {
			s.emitMarker(&out, false)
			violations = append(violations, Violation{
				Kind:      KindUnterminatedFence,
				BodyLines: bodyLines,
				StartLine: s.fenceStartLine,
				Stripped:  true,
			})
		} else {
			// Short unterminated fence: preserve opener + body, no closer;
			// record the violation so callers can WARN-log.
			s.emit(s.fenceOpener, &out)
			for _, l := range s.fencePending {
				s.emit(l, &out)
			}
			violations = append(violations, Violation{
				Kind:      KindUnterminatedFence,
				BodyLines: bodyLines,
				StartLine: s.fenceStartLine,
				Stripped:  false,
			})
		}
		s.fencePending = nil
		s.state = stateOutside
	case stateInIndentBlock:
		s.flushIndentPending(&out)
		s.state = stateOutside
	case stateDiscardingIndent:
		// Already handled at the (Max+1)th line trigger — nothing more to emit.
		s.state = stateOutside
	}

	return out.String(), violations
}

// processLine runs a single complete line through the state machine. It
// is the streaming analog of Filter's per-line loop body, with two
// differences: emitted bytes go to the supplied builder (not a local
// one), and lineIndex is a field that persists across calls.
func (s *Streamer) processLine(line lineRecord, out *strings.Builder, violations *[]Violation) {
	i := s.lineIndex
	s.lineIndex++

	switch s.state {

	case stateInFence:
		if isFenceLine(line.text) {
			bodyLines := len(s.fencePending)
			if bodyLines > MaxCodeLines {
				s.emitMarker(out, true)
				*violations = append(*violations, Violation{
					Kind:      KindFence,
					BodyLines: bodyLines,
					StartLine: s.fenceStartLine,
					Stripped:  true,
				})
			} else {
				s.emit(s.fenceOpener, out)
				for _, l := range s.fencePending {
					s.emit(l, out)
				}
				s.emit(line, out)
			}
			s.fencePending = nil
			s.state = stateOutside
			return
		}
		s.fencePending = append(s.fencePending, line)
		return

	case stateInIndentBlock:
		if isIndented(line.text) {
			s.indentPending = append(s.indentPending, line)
			if len(s.indentPending) > MaxCodeLines {
				s.emitMarker(out, true)
				*violations = append(*violations, Violation{
					Kind:      KindIndented,
					BodyLines: len(s.indentPending),
					StartLine: s.indentStartLine,
					Stripped:  true,
				})
				s.indentPending = nil
				s.state = stateDiscardingIndent
			}
			return
		}
		// Indented run ended at ≤ MaxCodeLines lines. Flush verbatim, drop
		// to outside state, and fall through to handle this line.
		s.flushIndentPending(out)
		s.state = stateOutside

	case stateDiscardingIndent:
		if isIndented(line.text) {
			return // swallow continued indented content
		}
		// Run is over; resume outside handling for this line.
		s.state = stateOutside
	}

	// stateOutside (possibly fallen through from indent flush / discard).
	if isFenceLine(line.text) {
		s.fenceStartLine = i
		s.fenceOpener = line
		s.fencePending = nil
		s.state = stateInFence
		return
	}
	if isIndented(line.text) && s.lastEmittedBlank {
		s.indentStartLine = i
		s.indentPending = []lineRecord{line}
		s.state = stateInIndentBlock
		return
	}
	s.emit(line, out)
}

func (s *Streamer) emit(l lineRecord, out *strings.Builder) {
	out.WriteString(l.text)
	if l.nl {
		out.WriteByte('\n')
	}
	s.lastEmittedBlank = isBlank(l.text)
}

func (s *Streamer) emitMarker(out *strings.Builder, withNewline bool) {
	out.WriteString(FirewallMarker)
	if withNewline {
		out.WriteByte('\n')
	}
	s.lastEmittedBlank = false
}

func (s *Streamer) flushIndentPending(out *strings.Builder) {
	// Reached only when an indented run ends at ≤ MaxCodeLines lines.
	// (The (Max+1)th line trips the marker and switches to
	// discardingIndent, so this slice never contains more than
	// MaxCodeLines entries here.)
	for _, l := range s.indentPending {
		s.emit(l, out)
	}
	s.indentPending = nil
}
