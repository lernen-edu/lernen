package progress

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	fileMode = 0o600
	dirMode  = 0o700
)

// Path returns the on-disk path for a curriculum's state.yaml under
// the progress root.
func Path(progressRoot, curriculumID string) string {
	return filepath.Join(progressRoot, curriculumID, "state.yaml")
}

// DefaultState returns an in-memory State for a curriculum that has
// no persisted state yet. The first /next or /chapter persists.
func DefaultState(curriculumID, firstChapterID string) *State {
	return &State{
		SchemaVersion:     CurrentSchemaVersion,
		CurriculumID:      curriculumID,
		UpdatedAt:         time.Now().UTC(),
		CurrentChapter:    firstChapterID,
		CompletedChapters: []ChapterCompletion{},
	}
}

// Load reads progress/<curriculumID>/state.yaml under progressRoot.
// Returns (nil, nil) when the file is absent; (nil, err) on malformed
// YAML or other read failure. If the on-disk state is an older schema
// version it is migrated in memory; a newer-than-binary version is a
// hard error.
func Load(progressRoot, curriculumID string) (*State, error) {
	path := Path(progressRoot, curriculumID)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("progress: read %s: %w", path, err)
	}
	var out State
	if err := yaml.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("progress: parse %s: %w", path, err)
	}
	if err := migrateState(&out); err != nil {
		return nil, fmt.Errorf("progress: %s: %w", path, err)
	}
	return &out, nil
}

// migrateState upgrades an in-memory State loaded from disk to
// CurrentSchemaVersion. v0/v1 → v2: stamp every existing demonstration
// Outcome = demonstrated_clean (the v1 structurer only ever emitted
// evidenced demonstrations, so this neither loses data nor over-claims).
// A version newer than this binary is a fail-closed error — never a
// silent downgrade or reset.
func migrateState(s *State) error {
	switch {
	case s.SchemaVersion > CurrentSchemaVersion:
		return fmt.Errorf("state.yaml was written by a newer Lernen (schema v%d); upgrade Lernen or move the file aside", s.SchemaVersion)
	case s.SchemaVersion == 0 || s.SchemaVersion == 1:
		for i := range s.CompletedChapters {
			for j := range s.CompletedChapters[i].Demonstrations {
				d := &s.CompletedChapters[i].Demonstrations[j]
				if d.Outcome == "" {
					d.Outcome = OutcomeDemonstratedClean
				}
			}
		}
		s.SchemaVersion = CurrentSchemaVersion
		return nil
	default:
		// 2..CurrentSchemaVersion: nothing to do.
		return nil
	}
}

// Save validates and atomically writes the state to disk. On a
// successful write, stamps UpdatedAt = now on the caller's State value
// so post-save inspection sees the timestamp. On any failure the
// caller's struct is left untouched.
func Save(progressRoot string, s *State) error {
	if s == nil {
		return fmt.Errorf("progress: Save: state is nil")
	}
	if err := s.Validate(); err != nil {
		return fmt.Errorf("progress: Save: %w", err)
	}
	if err := validateCurriculumIDForPath(s.CurriculumID); err != nil {
		return err
	}
	stamp := time.Now().UTC()
	toWrite := *s
	toWrite.UpdatedAt = stamp
	path := Path(progressRoot, s.CurriculumID)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return fmt.Errorf("progress: mkdir %s: %w", dir, err)
	}
	data, err := yaml.Marshal(&toWrite)
	if err != nil {
		return fmt.Errorf("progress: marshal: %w", err)
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("progress: create tempfile: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("progress: write tempfile: %w", err)
	}
	if err := tmp.Chmod(fileMode); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("progress: chmod tempfile: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("progress: sync tempfile: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("progress: close tempfile: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		cleanup()
		return fmt.Errorf("progress: rename %s → %s: %w", tmpPath, path, err)
	}
	// Stamp the caller's struct only after the file is in place.
	s.UpdatedAt = stamp
	return nil
}

// validateCurriculumIDForPath rejects curriculum IDs that would escape
// the progress root via the curriculum-ID subdirectory. Defense-in-
// depth: curriculum IDs come from manifests today, but Save constructs
// the on-disk path as <progressRoot>/<CurriculumID>/state.yaml.
func validateCurriculumIDForPath(curriculumID string) error {
	if strings.ContainsAny(curriculumID, `/\`) {
		return fmt.Errorf("progress: curriculum_id %q contains path separator", curriculumID)
	}
	if curriculumID == "." || curriculumID == ".." {
		return fmt.Errorf("progress: curriculum_id %q is a path traversal token", curriculumID)
	}
	return nil
}
