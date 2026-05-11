package progress

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/lernen-edu/lernen/internal/curriculum"
)

// indexOf returns the zero-based index of chapterID in the manifest's
// chapter order, or -1 when not present.
func indexOf(c *curriculum.Curriculum, chapterID string) int {
	for i := range c.Chapters {
		if c.Chapters[i].ID == chapterID {
			return i
		}
	}
	return -1
}

// NextChapter returns the chapter ID immediately after state.CurrentChapter
// in manifest order, or "" if the user is already on the last chapter.
// Returns an error when state.CurrentChapter is not in the manifest.
func NextChapter(state *State, c *curriculum.Curriculum) (string, error) {
	idx := indexOf(c, state.CurrentChapter)
	if idx < 0 {
		return "", fmt.Errorf("progress: current_chapter %q is not in the manifest", state.CurrentChapter)
	}
	if idx+1 >= len(c.Chapters) {
		return "", nil
	}
	return c.Chapters[idx+1].ID, nil
}

// PrevChapter returns the chapter ID immediately before state.CurrentChapter
// in manifest order, or "" if the user is already on the first chapter.
// Returns an error when state.CurrentChapter is not in the manifest.
func PrevChapter(state *State, c *curriculum.Curriculum) (string, error) {
	idx := indexOf(c, state.CurrentChapter)
	if idx < 0 {
		return "", fmt.Errorf("progress: current_chapter %q is not in the manifest", state.CurrentChapter)
	}
	if idx == 0 {
		return "", nil
	}
	return c.Chapters[idx-1].ID, nil
}

// ResolveChapter parses a user-typed argument into a chapter ID. Accepts:
//   - Full chapter ID (must match one in the manifest)
//   - 1-indexed integer position in the manifest's chapter order
//   - The word "prev" (alias for PrevChapter)
//   - The word "next" (alias for NextChapter)
//
// Empty input is an error; unknown IDs are an error; out-of-range numbers
// are an error; "prev" at the first chapter and "next" at the last chapter
// are errors.
func ResolveChapter(state *State, c *curriculum.Curriculum, arg string) (string, error) {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return "", fmt.Errorf("progress: ResolveChapter: empty argument")
	}
	switch strings.ToLower(arg) {
	case "prev":
		prev, err := PrevChapter(state, c)
		if err != nil {
			return "", err
		}
		if prev == "" {
			return "", fmt.Errorf("progress: already at the first chapter; no previous")
		}
		return prev, nil
	case "next":
		next, err := NextChapter(state, c)
		if err != nil {
			return "", err
		}
		if next == "" {
			return "", fmt.Errorf("progress: already at the last chapter; no next")
		}
		return next, nil
	}
	if n, err := strconv.Atoi(arg); err == nil {
		if n < 1 || n > len(c.Chapters) {
			return "", fmt.Errorf("progress: chapter number %d out of range (1..%d)", n, len(c.Chapters))
		}
		return c.Chapters[n-1].ID, nil
	}
	if indexOf(c, arg) >= 0 {
		return arg, nil
	}
	return "", fmt.Errorf("progress: unknown chapter %q (use a full chapter id, a 1-indexed number, or 'prev'/'next')", arg)
}
