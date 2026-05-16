package cli

import (
	"fmt"

	"github.com/lernen-edu/lernen/internal/progress"
)

// loadProgressFor resolves the progress directory and loads the persisted
// state for the given curriculumID. It returns both the resolved root path
// (needed for callers that also need to Save) and the loaded state (which
// may be nil when no progress has been recorded yet — callers are nil-safe).
//
// This helper is shared by practice.go (Task 10) and status.go (Task 11).
// runWork intentionally retains its own inline resolution so its behaviour is
// byte-identical and unaffected by any future changes to this helper.
func loadProgressFor(curriculumID string) (progressRoot string, state *progress.State, err error) {
	progressRoot, err = ProgressDir()
	if err != nil {
		return "", nil, fmt.Errorf("resolve progress dir: %w", err)
	}
	state, err = progress.Load(progressRoot, curriculumID)
	if err != nil {
		return "", nil, fmt.Errorf("load progress: %w", err)
	}
	return progressRoot, state, nil
}
