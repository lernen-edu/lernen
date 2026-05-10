package ingestion

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"golang.org/x/net/html"
	"gopkg.in/yaml.v3"

	"github.com/lernen-edu/lernen/internal/backends"
)

// Candidate is one proposed chapter from extraction. The mentor reads
// these as a system turn and discusses with the user; the structurer
// reads the dialogue (not these directly) at /wrap.
//
// The chapter is the primary unit. Subsections (when populated by the
// PDF outline path) lists the chapter's sub-bookmark titles so the
// mentor can present what's inside the chapter ("Chapter 7 — covers
// Defining Functions, Returning Values, Default Args"). Part (also
// populated only by the PDF outline path, when the source organizes
// chapters under Part I / Part II / ...) is a tag on the chapter
// indicating which Part it belongs to — Parts are not a grouping in
// their own right; the chapter remains the key detail.
//
// Subsections and Part are informational only — the structurer
// writes only Title + SourceLocator into ingestion.yaml. URL/HTML and
// LLM fallback paths leave both empty.
type Candidate struct {
	Title         string
	SourceLocator string
	Part          string
	Subsections   []string
}

// ExtractionResult is the outcome of /url or /pdf extraction. Method
// is one of: outline, semantic, llm. (paste is set by the structurer
// from the transcript, not by extraction.)
type ExtractionResult struct {
	Method     string
	Candidates []Candidate
}

// minHeuristicCandidates is the threshold below which a heuristic is
// considered "thin" and the LLM fallback fires.
const minHeuristicCandidates = 3

// htmlBodyCap is the maximum HTML body size sent to the LLM extraction
// fallback.
const htmlBodyCap = 500 * 1024

// httpFetchTimeout bounds /url HTTP GETs.
const httpFetchTimeout = 30 * time.Second

// FetchURL fetches the URL, tries semantic HTML extraction first, and
// falls back to an LLM extraction call against the body text if the
// heuristic produces fewer than minHeuristicCandidates entries.
func FetchURL(ctx context.Context, be backends.Backend, url string) (*ExtractionResult, error) {
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return nil, fmt.Errorf("ingestion: FetchURL: %q is not an http(s) URL", url)
	}
	body, err := httpGetWithTimeout(ctx, url)
	if err != nil {
		return nil, err
	}
	doc, err := html.Parse(strings.NewReader(string(body)))
	if err != nil {
		return nil, fmt.Errorf("ingestion: FetchURL: parse HTML: %w", err)
	}
	cand := semanticHTMLCandidates(doc)
	if len(cand) >= minHeuristicCandidates {
		return &ExtractionResult{Method: "semantic", Candidates: cand}, nil
	}
	text := htmlBodyText(doc)
	if len(text) > htmlBodyCap {
		text = text[:htmlBodyCap] + "\n\n[truncated]"
	}
	llmCand, err := llmExtractTOC(ctx, be, text)
	if err != nil {
		return nil, err
	}
	return &ExtractionResult{Method: "llm", Candidates: llmCand}, nil
}

func httpGetWithTimeout(ctx context.Context, url string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, httpFetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("ingestion: FetchURL: build request: %w", err)
	}
	req.Header.Set("User-Agent", "lernen/0.x")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ingestion: FetchURL: GET %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("ingestion: FetchURL: GET %s: HTTP status %d", url, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, htmlBodyCap*2))
}

// semanticHTMLCandidates walks the parsed HTML for likely TOC
// structures. Every <ol>/<ul> in the document is a candidate; the one
// with the highest score wins. Scoring rewards chapter-like link
// text (titles containing Chapter / Part / Lesson / Appendix /
// Section / Module) and penalizes lists whose links point at known
// social hosts (Twitter, Facebook, GitHub, etc.) — the dogfood case
// that motivated this rewrite was the heuristic locking onto the
// header nav of social/share links instead of the body's chapter
// list.
//
// If the best score is below scoreFloor (no chapter-like content
// AND no compelling length advantage), return nil so the LLM
// extraction fallback fires.
func semanticHTMLCandidates(root *html.Node) []Candidate {
	var best []Candidate
	bestScore := -1
	for _, list := range findDescendants(root, "ol", "ul") {
		cand := candidatesFromList(list)
		if len(cand) < minHeuristicCandidates {
			continue
		}
		score := scoreList(list, cand)
		if score > bestScore {
			bestScore = score
			best = cand
		}
	}
	if bestScore < htmlScoreFloor {
		return nil
	}
	return best
}

// htmlScoreFloor is the minimum score a candidate list must achieve
// to win the heuristic. A list with N non-social entries scores at
// least N (each entry contributes 1 point); social-only navs score
// negative; chapter-named entries get a +9 bonus apiece. The floor
// is set so a short social nav (5 entries, 5 social-URL hits) loses
// to even the most generic body-content list (3+ non-social entries)
// while still rejecting purely-social lists.
const htmlScoreFloor = 3

// scoreList computes a quality score for a candidate <ol>/<ul>.
//   +1  per entry  (base — long lists are TOC-like)
//   +9  per chapter-like title (Chapter / Part / Lesson / Appendix /
//       Section / Module + identifier)
//   -5  per link href pointing at a known social or sharing host
//
// Examples:
//   - Body TOC with 11 entries, 10 chapter-named, 0 social:
//       11 + 90 - 0 = 101
//   - Header social nav with 5 entries, 0 chapter, 5 social hits:
//       5 + 0 - 25 = -20
//   - Plain blog post list with 7 entries, 0 chapter, 0 social:
//       7 + 0 - 0 = 7  (passes floor — a plausible TOC)
//   - Footer with 3 internal-link entries:
//       3 + 0 - 0 = 3  (passes floor — accepted; mentor can challenge)
func scoreList(list *html.Node, cand []Candidate) int {
	chapterHits := 0
	for _, c := range cand {
		if looksLikeChapter(c.Title) {
			chapterHits++
		}
	}
	socialHits := 0
	for _, a := range findDescendants(list, "a") {
		for _, attr := range a.Attr {
			if attr.Key != "href" {
				continue
			}
			if looksLikeSocialURL(attr.Val) {
				socialHits++
				break
			}
		}
	}
	return len(cand) + chapterHits*9 - socialHits*5
}

// looksLikeSocialURL detects hrefs that point at social, sharing, or
// auth-redirect destinations — strong signal that the enclosing list
// is a header nav, not a TOC.
func looksLikeSocialURL(href string) bool {
	href = strings.ToLower(href)
	hosts := []string{
		"twitter.com", "x.com", "facebook.com", "fb.com", "instagram.com",
		"linkedin.com", "youtube.com", "youtu.be", "tiktok.com",
		"mastodon.", "bluesky.", "bsky.app",
		"github.com", "gitlab.com", "bitbucket.",
		"discord.gg", "discord.com", "reddit.com", "pinterest.com",
		"snapchat.com", "threads.net", "t.me/", "wa.me/", "whatsapp.com",
	}
	for _, h := range hosts {
		if strings.Contains(href, h) {
			return true
		}
	}
	return false
}

func candidatesFromList(list *html.Node) []Candidate {
	var out []Candidate
	for c := list.FirstChild; c != nil; c = c.NextSibling {
		if c.Type != html.ElementNode || c.Data != "li" {
			continue
		}
		title := ""
		for _, a := range findDescendants(c, "a") {
			title = strings.TrimSpace(textContent(a))
			if title != "" {
				break
			}
		}
		if title == "" {
			title = strings.TrimSpace(textContent(c))
		}
		if title == "" {
			continue
		}
		out = append(out, Candidate{Title: title, SourceLocator: title})
	}
	return out
}

func looksLikeChapter(s string) bool {
	low := strings.ToLower(s)
	for _, kw := range []string{"chapter ", "part ", "lesson ", "appendix ", "section ", "module "} {
		if strings.Contains(low, kw) {
			return true
		}
	}
	return false
}

func findDescendants(n *html.Node, tags ...string) []*html.Node {
	tagset := make(map[string]struct{}, len(tags))
	for _, t := range tags {
		tagset[t] = struct{}{}
	}
	var out []*html.Node
	walk(n, func(c *html.Node) {
		if c.Type == html.ElementNode {
			if _, ok := tagset[c.Data]; ok {
				out = append(out, c)
			}
		}
	})
	return out
}

func walk(n *html.Node, fn func(*html.Node)) {
	fn(n)
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		walk(c, fn)
	}
}

func textContent(n *html.Node) string {
	var sb strings.Builder
	walk(n, func(c *html.Node) {
		if c.Type == html.TextNode {
			sb.WriteString(c.Data)
		}
	})
	return sb.String()
}

func htmlBodyText(root *html.Node) string {
	var body *html.Node
	walk(root, func(n *html.Node) {
		if body == nil && n.Type == html.ElementNode && n.Data == "body" {
			body = n
		}
	})
	if body == nil {
		return textContent(root)
	}
	return textContent(body)
}

// llmExtractTOC sends the raw text to the backend with a short
// extraction prompt asking for a YAML list of chapter titles.
func llmExtractTOC(ctx context.Context, be backends.Backend, text string) ([]Candidate, error) {
	if be == nil {
		return nil, errors.New("ingestion: LLM extraction: backend is nil")
	}
	const prompt = `You are an extraction utility. Given the following raw text from a curriculum source (HTML body or PDF page extract), identify the table of contents (chapter list) and emit it as a YAML list.

Output ONLY a YAML list. Each entry is a map with a single key 'title'. Optionally include a 'source_locator' key for any obvious page or section pointer.

Example:
- title: "Getting Started"
- title: "Variables"
  source_locator: "Chapter 2, p. 15"

If you cannot identify a TOC, output an empty list ([]). No commentary, no markdown fences, no preamble.`

	messages := []backends.Message{{Role: backends.RoleUser, Content: text}}
	resp, err := be.Chat(ctx, messages, prompt)
	if err != nil {
		return nil, fmt.Errorf("ingestion: LLM extraction call: %w", err)
	}
	body := stripCodeFence(resp.Content)
	cand, err := parseLLMCandidates(body)
	if err != nil {
		return nil, fmt.Errorf("ingestion: parse LLM extraction output: %w; raw: %s", err, resp.Content)
	}
	return cand, nil
}

// parseLLMCandidates parses the LLM's YAML list response into
// Candidates. Tolerant: missing source_locator becomes the title.
func parseLLMCandidates(body string) ([]Candidate, error) {
	type llmEntry struct {
		Title         string `yaml:"title"`
		SourceLocator string `yaml:"source_locator,omitempty"`
	}
	var entries []llmEntry
	body = strings.TrimSpace(body)
	if body == "" || body == "[]" {
		return nil, nil
	}
	if err := yaml.Unmarshal([]byte(body), &entries); err != nil {
		return nil, err
	}
	out := make([]Candidate, 0, len(entries))
	for _, e := range entries {
		title := strings.TrimSpace(e.Title)
		if title == "" {
			continue
		}
		loc := strings.TrimSpace(e.SourceLocator)
		if loc == "" {
			loc = title
		}
		out = append(out, Candidate{Title: title, SourceLocator: loc})
	}
	return out, nil
}
