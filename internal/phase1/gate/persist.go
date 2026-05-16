package gate

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

func validateCurriculumIDForPath(id string) error {
	if id == "" || strings.ContainsAny(id, `/\`) || id == "." || id == ".." {
		return fmt.Errorf("gate: invalid curriculum id %q for path", id)
	}
	return nil
}

func Path(root, curriculumID string) string {
	return filepath.Join(root, curriculumID, "gate.yaml")
}
func SidecarPath(root, curriculumID string) string {
	return filepath.Join(root, curriculumID, "gate.inprogress.yaml")
}

func writeAtomic(path string, v any) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return fmt.Errorf("gate: mkdir %s: %w", dir, err)
	}
	b, err := yaml.Marshal(v)
	if err != nil {
		return fmt.Errorf("gate: marshal: %w", err)
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("gate: create tempfile: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("gate: write tempfile: %w", err)
	}
	if err := tmp.Chmod(fileMode); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("gate: chmod tempfile: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("gate: sync tempfile: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("gate: close tempfile: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("gate: rename %s -> %s: %w", tmpPath, path, err)
	}
	return nil
}

func SaveSidecar(root string, s *Sidecar) error {
	if err := s.Validate(); err != nil {
		return err
	}
	if err := validateCurriculumIDForPath(s.CurriculumID); err != nil {
		return err
	}
	sc := *s
	sc.SchemaVersion = CurrentSchemaVersion
	return writeAtomic(SidecarPath(root, sc.CurriculumID), &sc)
}

func LoadSidecar(root, curriculumID string) (*Sidecar, error) {
	b, err := os.ReadFile(SidecarPath(root, curriculumID))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var s Sidecar
	if err := yaml.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("gate: parse sidecar: %w", err)
	}
	if err := s.Validate(); err != nil {
		return nil, err
	}
	return &s, nil
}

func ClearSidecar(root, curriculumID string) error {
	err := os.Remove(SidecarPath(root, curriculumID))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

func LoadLog(root, curriculumID string) (*Log, error) {
	b, err := os.ReadFile(Path(root, curriculumID))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var l Log
	if err := yaml.Unmarshal(b, &l); err != nil {
		return nil, fmt.Errorf("gate: parse log: %w", err)
	}
	if err := l.Validate(); err != nil {
		return nil, err
	}
	return &l, nil
}

func AppendAttempt(root, curriculumID string, att Attempt) error {
	if err := validateCurriculumIDForPath(curriculumID); err != nil {
		return err
	}
	lg, err := LoadLog(root, curriculumID)
	if err != nil {
		return err
	}
	if lg == nil {
		lg = &Log{SchemaVersion: CurrentSchemaVersion, CurriculumID: curriculumID}
	}
	lg.Attempts = append(lg.Attempts, att)
	lg.UpdatedAt = time.Now().UTC()
	lg.SchemaVersion = CurrentSchemaVersion
	return writeAtomic(Path(root, curriculumID), lg)
}

// NextAttemptNumber = len(log)+1 (1-based), independent of resume.
func NextAttemptNumber(root, curriculumID string) (int, error) {
	lg, err := LoadLog(root, curriculumID)
	if err != nil {
		return 0, err
	}
	if lg == nil {
		return 1, nil
	}
	return len(lg.Attempts) + 1, nil
}
