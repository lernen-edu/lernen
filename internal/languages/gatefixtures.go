package languages

import (
	"bytes"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// SourceAttribution records the OSS provenance of a comprehension
// snippet (spec §8): short transformative excerpts of permissively
// licensed code, attributed.
type SourceAttribution struct {
	Project string `yaml:"project"`
	License string `yaml:"license"`
	URL     string `yaml:"url"`
}

type BuildFixture struct {
	ID           string        `yaml:"id"`
	Prompt       string        `yaml:"prompt"`
	TestScaffold string        `yaml:"test_scaffold"`
	TimeBudget   time.Duration `yaml:"-"`
	TimeBudgetS  string        `yaml:"time_budget"`
}

type IssueKeyEntry struct {
	Description string `yaml:"description"`
}

type ComprehensionFixture struct {
	ID             string            `yaml:"id"`
	Language       string            `yaml:"language"`
	Snippet        string            `yaml:"snippet"`
	ExpectedOutput string            `yaml:"expected_output"`
	ExpectedIssues []IssueKeyEntry   `yaml:"expected_issues"`
	Source         SourceAttribution `yaml:"source"`
}

type DebugFixture struct {
	ID            string `yaml:"id"`
	Tier          int    `yaml:"tier"`
	BrokenProgram string `yaml:"broken_program"`
	TestScaffold  string `yaml:"test_scaffold"`
}

type GateFixtures struct {
	Build         []BuildFixture
	Comprehension []ComprehensionFixture
	Debug         []DebugFixture
}

// Cardinality floors so deterministic per-attempt selection (1 build +
// 3 comprehension + 3 debug, 1/tier) actually rotates across the
// re-attemptable gate.
const (
	minBuild         = 3
	minComprehension = 5
	minDebugPerTier  = 2
)

func loadYAMLDir[T any](fsys fs.FS, dir string) ([]T, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, fmt.Errorf("gate fixtures: read %s: %w", dir, err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".yaml") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names) // explicit: deterministic order is load-bearing for gate selection (do not rely on fs.FS ordering)
	out := make([]T, 0, len(names))
	for _, n := range names {
		b, err := fs.ReadFile(fsys, path.Join(dir, n))
		if err != nil {
			return nil, fmt.Errorf("gate fixtures: read %s/%s: %w", dir, n, err)
		}
		var v T
		dec := yaml.NewDecoder(bytes.NewReader(b))
		dec.KnownFields(true)
		if err := dec.Decode(&v); err != nil {
			return nil, fmt.Errorf("gate fixtures: parse %s/%s: %w", dir, n, err)
		}
		out = append(out, v)
	}
	return out, nil
}

// LoadGateFixtures loads + validates a fixture bank from any fs.FS
// (the Python adapter passes its embed.FS; tests pass fstest.MapFS).
func LoadGateFixtures(fsys fs.FS) (GateFixtures, error) {
	var gf GateFixtures
	var err error
	if gf.Build, err = loadYAMLDir[BuildFixture](fsys, "build"); err != nil {
		return GateFixtures{}, err
	}
	if gf.Comprehension, err = loadYAMLDir[ComprehensionFixture](fsys, "comprehension"); err != nil {
		return GateFixtures{}, err
	}
	if gf.Debug, err = loadYAMLDir[DebugFixture](fsys, "debug"); err != nil {
		return GateFixtures{}, err
	}
	if err := gf.validate(); err != nil {
		return GateFixtures{}, err
	}
	return gf, nil
}

func (gf *GateFixtures) validate() error {
	if len(gf.Build) < minBuild {
		return fmt.Errorf("gate fixtures: build: need >=%d, got %d", minBuild, len(gf.Build))
	}
	for i := range gf.Build {
		b := &gf.Build[i]
		if strings.TrimSpace(b.ID) == "" || strings.TrimSpace(b.Prompt) == "" || strings.TrimSpace(b.TestScaffold) == "" {
			return fmt.Errorf("gate fixtures: build[%d] %q: id/prompt/test_scaffold required", i, b.ID)
		}
		d, err := time.ParseDuration(b.TimeBudgetS)
		if err != nil || d <= 0 {
			return fmt.Errorf("gate fixtures: build %q: invalid time_budget %q", b.ID, b.TimeBudgetS)
		}
		b.TimeBudget = d
	}
	if len(gf.Comprehension) < minComprehension {
		return fmt.Errorf("gate fixtures: comprehension: need >=%d, got %d", minComprehension, len(gf.Comprehension))
	}
	for i, c := range gf.Comprehension {
		if strings.TrimSpace(c.ID) == "" || strings.TrimSpace(c.Snippet) == "" || strings.TrimSpace(c.ExpectedOutput) == "" {
			return fmt.Errorf("gate fixtures: comprehension[%d] %q: id/snippet/expected_output required", i, c.ID)
		}
		if len(c.ExpectedIssues) == 0 {
			return fmt.Errorf("gate fixtures: comprehension[%d] %q: >=1 expected_issues required", i, c.ID)
		}
		if strings.TrimSpace(c.Source.Project) == "" || strings.TrimSpace(c.Source.License) == "" || strings.TrimSpace(c.Source.URL) == "" {
			return fmt.Errorf("gate fixtures: comprehension[%d] %q: source project/license/url required (spec §8)", i, c.ID)
		}
	}
	perTier := map[int]int{}
	for i, d := range gf.Debug {
		if d.Tier < 1 || d.Tier > 3 {
			return fmt.Errorf("gate fixtures: debug %q: tier must be 1..3, got %d", d.ID, d.Tier)
		}
		if strings.TrimSpace(d.ID) == "" || strings.TrimSpace(d.BrokenProgram) == "" || strings.TrimSpace(d.TestScaffold) == "" {
			return fmt.Errorf("gate fixtures: debug[%d] %q: id/broken_program/test_scaffold required", i, d.ID)
		}
		perTier[d.Tier]++
	}
	for tier := 1; tier <= 3; tier++ {
		if perTier[tier] < minDebugPerTier {
			return fmt.Errorf("gate fixtures: debug tier %d: need >=%d, got %d", tier, minDebugPerTier, perTier[tier])
		}
	}
	return nil
}
