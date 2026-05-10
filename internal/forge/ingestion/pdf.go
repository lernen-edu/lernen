package ingestion

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"regexp"
	"sort"
	"strings"

	"rsc.io/pdf"

	"github.com/lernen-edu/lernen/internal/backends"
)

// pdfTextPagesScan is the number of pages from the start of the PDF
// that the Contents-page heuristic and LLM fallback consider.
const pdfTextPagesScan = 30

// pdfTextCap caps the text size sent to the LLM extraction fallback.
const pdfTextCap = 500 * 1024

// ReadPDF opens the PDF at path, tries the /Outline tree first, then
// a Contents-page heuristic on the first pdfTextPagesScan pages, and
// falls back to an LLM extraction call against the same text.
func ReadPDF(ctx context.Context, be backends.Backend, path string) (*ExtractionResult, error) {
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("ingestion: ReadPDF: file does not exist: %s", path)
		}
		return nil, fmt.Errorf("ingestion: ReadPDF: stat %s: %w", path, err)
	}
	r, err := pdf.Open(path)
	if err != nil {
		// rsc.io/pdf surfaces encryption as "malformed PDF: 256-bit
		// encryption key" — the lowercase substring "encrypt" matches.
		msg := err.Error()
		low := strings.ToLower(msg)
		if strings.Contains(low, "encrypt") {
			return nil, fmt.Errorf("ingestion: ReadPDF: %s is encrypted; decrypt locally or use /paste", path)
		}
		return nil, fmt.Errorf("ingestion: ReadPDF: open %s: %w", path, err)
	}

	// Step 1: /Outline tree.
	if cand := outlineCandidates(r); len(cand) >= minHeuristicCandidates {
		return &ExtractionResult{Method: "outline", Candidates: cand}, nil
	}

	// Step 2: text from first N pages.
	text, err := pdfFirstPagesText(r, pdfTextPagesScan)
	if err != nil {
		return nil, fmt.Errorf("ingestion: ReadPDF: extract text from %s: %w", path, err)
	}
	if strings.TrimSpace(text) == "" {
		return nil, fmt.Errorf("ingestion: ReadPDF: %s has no extractable text (scanned PDF? use /paste)", path)
	}
	if cand := contentsPageCandidates(text); len(cand) >= minHeuristicCandidates {
		return &ExtractionResult{Method: "semantic", Candidates: cand}, nil
	}

	// Step 3: LLM fallback.
	if len(text) > pdfTextCap {
		text = text[:pdfTextCap] + "\n\n[truncated]"
	}
	llmCand, err := llmExtractTOC(ctx, be, text)
	if err != nil {
		return nil, err
	}
	return &ExtractionResult{Method: "llm", Candidates: llmCand}, nil
}

// partTitleRe matches outline entries that look like Parts/Sections/
// Units/Modules — dividers/groupings that aren't themselves chapters.
// Case-insensitive; tolerates Roman or arabic numerals.
//
// outlineCandidates skips entries matching this pattern from the
// returned chapter list and uses them as Part tags for following
// chapter siblings (Part-as-divider) or drills into them (Part-as-
// container, when the entry has children).
var partTitleRe = regexp.MustCompile(`(?i)^\s*(part|section|unit|module)\s+([ivx]+|\d+)\b`)

// endMatterRe matches outline entries that mark end-matter sections
// (appendixes, index, glossary, etc.). These ARE chapters in their
// own right (the user wants them in the chapter list), but they sit
// outside any Part — typically after all Parts. When outlineCandidates
// sees one, it resets currentPart so the entry isn't mis-tagged with
// the previous Part.
var endMatterRe = regexp.MustCompile(`(?i)^\s*(appendix|appendices|index|glossary|references|bibliography|notes|epilogue|afterword|colophon)\b`)

// outlineCandidates extracts the chapter list from the PDF's /Outline
// (bookmark) tree. The chapter is the primary unit; Parts are tags,
// not container groupings. The walk handles two real-world layouts:
//
//   - Part-as-container (Programming Perl style): Parts contain
//     chapters as nested children. Drilling into the Part yields the
//     chapters; each gets the Part title as a tag.
//
//   - Part-as-divider (Python Crash Course style): Parts and chapters
//     are siblings at the same outline level. The Part bookmark is a
//     divider/marker between groups of chapters; the chapters that
//     follow (until the next Part or end-of-level) inherit the Part
//     as their tag.
//
// In both cases, Parts are NOT chapters — they're skipped from the
// returned list. Top-level non-Part entries (frontmatter, appendixes
// outside any Part) become chapters with no Part tag.
//
// Recursing into every descendant produces hundreds of per-page
// bookmarks instead of the chapter list, so the walk stops at chapter
// level. Sub-bookmarks of each chapter become Subsections on the
// returned Candidate (informational, not persisted to ingestion.yaml —
// the structurer writes only Title + SourceLocator).
func outlineCandidates(r *pdf.Reader) []Candidate {
	root := r.Outline()
	top := root.Child

	var out []Candidate
	currentPart := ""
	for _, entry := range top {
		title := strings.TrimSpace(entry.Title)
		if title == "" {
			continue
		}
		if partTitleRe.MatchString(title) {
			if len(entry.Child) > 0 {
				// Part-as-container: drill into the Part's children
				// (those are the chapters).
				for _, ch := range entry.Child {
					if c := chapterCandidate(ch, title); c != nil {
						out = append(out, *c)
					}
				}
				// After drilling, reset — sibling top-level entries
				// that follow this Part are not under it.
				currentPart = ""
			} else {
				// Part-as-divider: the Part itself contributes no
				// chapter; it sets the tag for following siblings.
				currentPart = title
			}
			continue
		}
		// End-matter section (Appendix, Index, Glossary, ...): IS a
		// chapter in its own right, but resets currentPart since
		// end-matter sits outside any Part.
		if endMatterRe.MatchString(title) {
			currentPart = ""
		}
		// Regular chapter (or end-matter) at top level.
		if c := chapterCandidate(entry, currentPart); c != nil {
			out = append(out, *c)
		}
	}
	if len(out) > 0 {
		return out
	}
	if strings.TrimSpace(root.Title) != "" {
		return []Candidate{{Title: strings.TrimSpace(root.Title), SourceLocator: root.Title}}
	}
	return nil
}

// chapterCandidate renders one outline entry as a Candidate at the
// chapter level. The chapter is the primary unit: Title and
// SourceLocator are the chapter's own title (no Part prefix). When
// the chapter sits under a Part-style outer ring, partTitle is set
// as a separate tag on the Candidate — informational, surfaced in
// the rendered system turn but not embedded in the locator. Sub-
// bookmark titles are captured as Subsections one level down — never
// deeper, so a section's own sub-headings don't bleed into the
// chapter's Subsections list.
func chapterCandidate(o pdf.Outline, partTitle string) *Candidate {
	title := strings.TrimSpace(o.Title)
	if title == "" {
		return nil
	}
	c := &Candidate{
		Title:         title,
		SourceLocator: title,
		Part:          partTitle,
	}
	for _, sub := range o.Child {
		st := strings.TrimSpace(sub.Title)
		if st != "" {
			c.Subsections = append(c.Subsections, st)
		}
	}
	return c
}

// pdfFirstPagesText concatenates extracted text from up to maxPages
// from the start of the PDF. Text items on each page are sorted by Y
// (descending, top-to-bottom) then X (ascending, left-to-right) and
// grouped into lines by Y coordinate proximity.
func pdfFirstPagesText(r *pdf.Reader, maxPages int) (string, error) {
	n := r.NumPage()
	if n > maxPages {
		n = maxPages
	}
	var sb strings.Builder
	for i := 1; i <= n; i++ {
		page := r.Page(i)
		if page.V.IsNull() {
			continue
		}
		pageTxt := extractPageText(page)
		if pageTxt != "" {
			sb.WriteString(pageTxt)
			sb.WriteByte('\n')
		}
	}
	return sb.String(), nil
}

// extractPageText converts a pdf.Page's content to a plain-text string
// by sorting text items into reading order and grouping by line.
func extractPageText(page pdf.Page) string {
	content := page.Content()
	items := content.Text
	if len(items) == 0 {
		return ""
	}

	// Sort top-to-bottom, left-to-right.
	sort.SliceStable(items, func(i, j int) bool {
		ti, tj := items[i], items[j]
		if math.Abs(ti.Y-tj.Y) > 2 {
			return ti.Y > tj.Y
		}
		return ti.X < tj.X
	})

	var sb strings.Builder
	var lastY float64 = math.MaxFloat64
	var lastX float64 = 0
	for _, t := range items {
		if t.S == "" {
			continue
		}
		if math.Abs(t.Y-lastY) > 2 {
			// New line.
			if sb.Len() > 0 {
				sb.WriteByte('\n')
			}
			lastX = 0
		} else if lastX > 0 && t.W > 0 && t.X > lastX+t.W*0.5 {
			// Same line, meaningful horizontal gap — insert space.
			sb.WriteByte(' ')
		}
		sb.WriteString(t.S)
		lastY = t.Y
		lastX = t.X + t.W
	}
	return sb.String()
}

// contentsPageCandidates scans extracted text for a "Contents" page
// and parses chapter-like lines from it.
func contentsPageCandidates(text string) []Candidate {
	idx := contentsHeaderIndex(text)
	if idx < 0 {
		return nil
	}
	tail := text[idx:]
	if len(tail) > 4096 {
		tail = tail[:4096]
	}
	lines := strings.Split(tail, "\n")
	if len(lines) > 0 {
		lines = lines[1:] // skip the "Contents" header line itself
	}
	var out []Candidate
	for _, ln := range lines {
		title := chapterFromLine(ln)
		if title != "" {
			out = append(out, Candidate{Title: title, SourceLocator: title})
		}
	}
	return out
}

func contentsHeaderIndex(text string) int {
	low := strings.ToLower(text)
	for _, marker := range []string{"\ntable of contents", "\ncontents\n", "\n contents\n"} {
		if i := strings.Index(low, marker); i >= 0 {
			return i + 1
		}
	}
	if strings.HasPrefix(low, "table of contents") || strings.HasPrefix(low, "contents") {
		return 0
	}
	return -1
}

// chapterRe matches lines that look like chapter entries in a TOC, handling both:
//   - Spaced:      "Chapter 1  Getting Started"
//   - Concatenated: "Chapter1GettingStarted" (gofpdf character-level text items)
//   - Numbered:    "1. Getting Started ......... 1"
//   - Roman:       "Part I  Introduction"
var chapterRe = regexp.MustCompile(`^(?:Chapter|Part|Lesson|Section)\s*(\d+|[IVX]+)\s*[.:\-]?\s*(.+)$`)

// numberedLineRe matches lines like "1. Getting Started .... 5" or "1 Getting Started".
var numberedLineRe = regexp.MustCompile(`^\s*[\dIVX]+\s*[.)\-]?\s+(.+?)(?:\s*\.+\s*\d+|\s+\d+)?\s*$`)

func chapterFromLine(line string) string {
	line = strings.TrimSpace(line)
	if line == "" {
		return ""
	}
	// Try chapter/part/lesson keyword form first.
	if m := chapterRe.FindStringSubmatch(line); m != nil {
		title := strings.TrimSpace(m[2])
		title = strings.TrimRight(title, ". 0123456789")
		title = strings.TrimSpace(title)
		if title != "" {
			return title
		}
	}
	// Try plain numbered form.
	if m := numberedLineRe.FindStringSubmatch(line); m != nil {
		title := strings.TrimSpace(m[1])
		title = strings.TrimRight(title, ". 0123456789")
		title = strings.TrimSpace(title)
		if title != "" {
			return title
		}
	}
	return ""
}
