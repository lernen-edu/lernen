package reflection

import (
	"bytes"
	_ "embed"
	"fmt"
	"strings"
	"text/template"

	"github.com/lernen-edu/lernen/internal/forge/calibration"
	"github.com/lernen-edu/lernen/internal/forge/goals"
	"github.com/lernen-edu/lernen/internal/forge/ingestion"
	"github.com/lernen-edu/lernen/internal/forge/recommendation"
	"github.com/lernen-edu/lernen/internal/forge/scaffold"
)

//go:embed reflection.md
var reflectionPromptTemplate string

var reflectionTmpl = template.Must(template.New("reflection").Parse(reflectionPromptTemplate))

//go:embed reflection_structurer.md
var reflectionStructurerPromptTemplate string

// ReflectionStructurerSystemPrompt returns the structurer system prompt
// verbatim. It does not interpolate anything — the structured input
// (transcript + loaded forge state) is passed via Chat messages.
func ReflectionStructurerSystemPrompt() string {
	return reflectionStructurerPromptTemplate
}

// ReflectionPromptInput is the union of state the mentor prompt embeds.
// It is constructed by reflection.RunReflection from the six loaded
// profile artifacts and is also the input to ReflectionSystemPrompt for
// golden-file testing.
type ReflectionPromptInput struct {
	Goals               *goals.Goals
	StartingPoint       *calibration.StartingPoint
	Recommendation      *recommendation.Recommendation
	Ingestion           *ingestion.Ingestion
	ClassifiedChapters  *scaffold.ClassifiedChapters
	Competencies        []scaffold.Competency
	Scaffolds           []scaffold.ChapterScaffold
	DefaultCurriculumID string
	// derived for template convenience
	OrientationCount int
	ContentCount     int
}

// ReflectionSystemPrompt renders the mentor system prompt. The input is
// validated for non-nil pointers (the orchestrator must load all six
// artifacts before calling).
func ReflectionSystemPrompt(in ReflectionPromptInput) (string, error) {
	if in.Goals == nil {
		return "", fmt.Errorf("reflection: ReflectionSystemPrompt: Goals is nil")
	}
	if in.StartingPoint == nil {
		return "", fmt.Errorf("reflection: ReflectionSystemPrompt: StartingPoint is nil")
	}
	if in.Recommendation == nil {
		return "", fmt.Errorf("reflection: ReflectionSystemPrompt: Recommendation is nil")
	}
	if in.Ingestion == nil {
		return "", fmt.Errorf("reflection: ReflectionSystemPrompt: Ingestion is nil")
	}
	if in.ClassifiedChapters == nil {
		return "", fmt.Errorf("reflection: ReflectionSystemPrompt: ClassifiedChapters is nil")
	}
	for _, cl := range in.ClassifiedChapters.Classifications {
		switch cl.Kind {
		case "orientation":
			in.OrientationCount++
		case "content":
			in.ContentCount++
		}
	}
	var buf bytes.Buffer
	if err := reflectionTmpl.Execute(&buf, in); err != nil {
		return "", fmt.Errorf("reflection: render mentor prompt: %w", err)
	}
	return buf.String(), nil
}

// DeriveDefaultCurriculumID slugifies a source title (typically
// Recommendation.CurriculumName) into a curriculum-id candidate.
// Lowercase, alphanumeric, hyphens; trimmed to 24 chars. Strips
// parentheticals and version numbers.
//
// Examples:
//
//	"Python Crash Course (3rd edition)" → "python-crash-course"
//	"Automate the Boring Stuff with Python" → "automate-the-boring"
func DeriveDefaultCurriculumID(sourceTitle string) string {
	s := strings.ToLower(sourceTitle)
	// strip parentheticals
	for {
		open := strings.Index(s, "(")
		close := strings.Index(s, ")")
		if open < 0 || close < 0 || close <= open {
			break
		}
		s = s[:open] + s[close+1:]
	}
	// keep [a-z0-9] and spaces; replace everything else with a space.
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteRune(' ')
		}
	}
	parts := strings.Fields(b.String())
	if len(parts) == 0 {
		return "curriculum"
	}
	out := strings.Join(parts, "-")
	if len(out) > 24 {
		// trim to the last hyphen-bounded prefix that fits
		cut := out[:24]
		if i := strings.LastIndex(cut, "-"); i > 0 {
			cut = cut[:i]
		}
		out = cut
	}
	// strip trailing/leading hyphens defensively
	out = strings.Trim(out, "-")
	if out == "" {
		return "curriculum"
	}
	return out
}
