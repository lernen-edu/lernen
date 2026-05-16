package gate

import (
	"os"
	"path/filepath"
	"time"
)

// materialize lays out a gate workdir identical to the M4c practice
// contract (solution.py the learner edits + verbatim test_exercise.py),
// under the gate subtree so it never collides with practice.
func materialize(dataRoot, curriculumID, fixtureID, prompt, testScaffold string) (string, error) {
	ts := time.Now().UTC().Format("20060102T150405Z")
	wd := filepath.Join(dataRoot, "gate", curriculumID, fixtureID+"-"+ts)
	if err := os.MkdirAll(wd, 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(wd, "solution.py"), nil, 0o644); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(wd, "test_exercise.py"), []byte(testScaffold), 0o644); err != nil {
		return "", err
	}
	body := prompt + "\n\n---\nEdit solution.py in your own editor, then /submit.\n"
	if err := os.WriteFile(filepath.Join(wd, "PROMPT.md"), []byte(body), 0o644); err != nil {
		return "", err
	}
	return wd, nil
}

func writeBrokenProgram(wd, program string) error {
	return os.WriteFile(filepath.Join(wd, "solution.py"), []byte(program), 0o644)
}
