package reflection

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/lernen-edu/lernen/internal/curriculum"
	"github.com/lernen-edu/lernen/internal/forge/ingestion"
	"github.com/lernen-edu/lernen/internal/forge/recommendation"
	"github.com/lernen-edu/lernen/internal/forge/scaffold"
)

const (
	fileMode = 0o600
	dirMode  = 0o700
)

// Finalize transforms the profile artifacts into a runtime-shaped
// manifest at <manifestRoot>/<r.Curriculum.ID>/. Atomic via tmp-dir +
// rename; validates the published manifest via curriculum.Load, with
// rollback on validation failure.
//
// Inputs:
//
//	profileDir   — the forge profile dir containing the six prior
//	               stage outputs.
//	manifestRoot — the directory under which the manifest dir is
//	               created (typically internal/cli.ManifestsDir()).
//	r            — the persisted reflection result; carries the
//	               curriculum-id, name, and forge_log.md content.
//	forgeVersion — version string for the synthesized curriculum.yaml.
//	authoredBy   — value to write into curriculum.yaml's author_attribution
//	               and authored_by fields.
//
// Returns the absolute path to the published manifest dir.
func Finalize(profileDir, manifestRoot string, r *ReflectionResult, forgeVersion, authoredBy string) (string, error) {
	if r == nil {
		return "", fmt.Errorf("reflection: Finalize: r is nil")
	}
	if err := r.Validate(); err != nil {
		return "", fmt.Errorf("reflection: Finalize: invalid result: %w", err)
	}

	target := filepath.Join(manifestRoot, r.Curriculum.ID)
	if _, err := os.Stat(target); err == nil {
		return "", fmt.Errorf("reflection: Finalize: manifest already exists at %s; use --reset-stage=reflection to re-author and pick a different curriculum-id, or remove the existing dir and re-run", target)
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("reflection: Finalize: stat target: %w", err)
	}

	rec, err := loadRecommendationFile(profileDir)
	if err != nil {
		return "", err
	}
	ing, err := loadIngestionFile(profileDir)
	if err != nil {
		return "", err
	}
	mc, err := loadManifestCompetenciesFile(profileDir)
	if err != nil {
		return "", err
	}
	cc, err := loadClassifiedChaptersFile(profileDir)
	if err != nil {
		return "", err
	}

	tmp := filepath.Join(manifestRoot, fmt.Sprintf(".tmp-%s-%s", r.Curriculum.ID, time.Now().UTC().Format("20060102T150405Z")))
	if err := os.MkdirAll(tmp, dirMode); err != nil {
		return "", fmt.Errorf("reflection: Finalize: mkdir tmp: %w", err)
	}
	// On any return path that didn't reach the successful rename,
	// remove tmp. After a successful rename tmp no longer exists,
	// so RemoveAll silently no-ops.
	defer func() {
		_ = os.RemoveAll(tmp)
	}()

	meta := synthesizeCurriculum(r, ing, rec, forgeVersion, authoredBy)
	if err := writeYAML(filepath.Join(tmp, "curriculum.yaml"), meta); err != nil {
		return "", err
	}

	comps := transformCompetencies(mc.Competencies)
	if err := writeYAML(filepath.Join(tmp, "competencies.yaml"), comps); err != nil {
		return "", err
	}

	chDir := filepath.Join(tmp, "chapters")
	if err := os.MkdirAll(chDir, dirMode); err != nil {
		return "", fmt.Errorf("reflection: Finalize: mkdir chapters: %w", err)
	}
	for _, cl := range cc.Classifications {
		raw, err := os.ReadFile(filepath.Join(profileDir, "chapter_scaffolds", cl.ChapterID+".yaml"))
		if err != nil {
			if os.IsNotExist(err) {
				continue // skipped chapter
			}
			return "", fmt.Errorf("reflection: Finalize: read scaffold %s: %w", cl.ChapterID, err)
		}
		var s scaffold.ChapterScaffold
		if err := yaml.Unmarshal(raw, &s); err != nil {
			return "", fmt.Errorf("reflection: Finalize: unmarshal scaffold %s: %w", cl.ChapterID, err)
		}
		if s.Deferred {
			continue
		}
		ch := transformChapter(s)
		if err := writeYAML(filepath.Join(chDir, s.ID+".yaml"), ch); err != nil {
			return "", err
		}
	}

	if err := os.WriteFile(filepath.Join(tmp, "forge_log.md"), []byte(r.ForgeLog), fileMode); err != nil {
		return "", fmt.Errorf("reflection: Finalize: write forge_log.md: %w", err)
	}

	if err := os.Rename(tmp, target); err != nil {
		return "", fmt.Errorf("reflection: Finalize: rename tmp → target: %w", err)
	}

	// Validate via the runtime loader. On failure, roll back the target.
	if _, loadErr := curriculum.Load(target); loadErr != nil {
		_ = os.RemoveAll(target)
		return "", fmt.Errorf("reflection: Finalize: published manifest failed runtime loader validation (manifest removed): %w", loadErr)
	}

	return target, nil
}

func writeYAML(path string, v any) error {
	raw, err := yaml.Marshal(v)
	if err != nil {
		return fmt.Errorf("reflection: Finalize: marshal %s: %w", filepath.Base(path), err)
	}
	if err := os.WriteFile(path, raw, fileMode); err != nil {
		return fmt.Errorf("reflection: Finalize: write %s: %w", filepath.Base(path), err)
	}
	return nil
}

func loadRecommendationFile(profileDir string) (*recommendation.Recommendation, error) {
	raw, err := os.ReadFile(filepath.Join(profileDir, "recommendation.yaml"))
	if err != nil {
		return nil, fmt.Errorf("reflection: Finalize: read recommendation.yaml: %w", err)
	}
	var rec recommendation.Recommendation
	if err := yaml.Unmarshal(raw, &rec); err != nil {
		return nil, fmt.Errorf("reflection: Finalize: unmarshal recommendation.yaml: %w", err)
	}
	return &rec, nil
}

func loadIngestionFile(profileDir string) (*ingestion.Ingestion, error) {
	raw, err := os.ReadFile(filepath.Join(profileDir, "ingestion.yaml"))
	if err != nil {
		return nil, fmt.Errorf("reflection: Finalize: read ingestion.yaml: %w", err)
	}
	var ing ingestion.Ingestion
	if err := yaml.Unmarshal(raw, &ing); err != nil {
		return nil, fmt.Errorf("reflection: Finalize: unmarshal ingestion.yaml: %w", err)
	}
	return &ing, nil
}

func loadManifestCompetenciesFile(profileDir string) (*scaffold.ManifestCompetencies, error) {
	raw, err := os.ReadFile(filepath.Join(profileDir, "manifest_competencies.yaml"))
	if err != nil {
		return nil, fmt.Errorf("reflection: Finalize: read manifest_competencies.yaml: %w", err)
	}
	var mc scaffold.ManifestCompetencies
	if err := yaml.Unmarshal(raw, &mc); err != nil {
		return nil, fmt.Errorf("reflection: Finalize: unmarshal manifest_competencies.yaml: %w", err)
	}
	return &mc, nil
}

func loadClassifiedChaptersFile(profileDir string) (*scaffold.ClassifiedChapters, error) {
	raw, err := os.ReadFile(filepath.Join(profileDir, "classified_chapters.yaml"))
	if err != nil {
		return nil, fmt.Errorf("reflection: Finalize: read classified_chapters.yaml: %w", err)
	}
	var cc scaffold.ClassifiedChapters
	if err := yaml.Unmarshal(raw, &cc); err != nil {
		return nil, fmt.Errorf("reflection: Finalize: unmarshal classified_chapters.yaml: %w", err)
	}
	return &cc, nil
}
