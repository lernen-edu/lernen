package gate

import (
	"fmt"
	"time"
)

const CurrentSchemaVersion = 1

type Component string

const (
	ComponentBuild         Component = "build"
	ComponentComprehension Component = "comprehension"
	ComponentDebug         Component = "debug"
)

func ComponentOrder() []Component {
	return []Component{ComponentBuild, ComponentComprehension, ComponentDebug}
}

type ComponentOutcome string

const (
	OutcomePass       ComponentOutcome = "pass"
	OutcomeFail       ComponentOutcome = "fail"
	OutcomeInfraError ComponentOutcome = "infra_error"
)

// Terminal reports whether an outcome finalizes its component.
// infra_error is NON-terminal: the attempt stays resumable and a
// broken environment never manufactures a FAIL (spec §2.4).
func (o ComponentOutcome) Terminal() bool {
	return o == OutcomePass || o == OutcomeFail
}

type FixtureSet struct {
	Build         string   `yaml:"build"`
	Comprehension []string `yaml:"comprehension"`
	Debug         []string `yaml:"debug"`
}

type PreconditionSnapshot struct {
	Met             bool `yaml:"met"`
	FoundationMet   int  `yaml:"foundation_met"`
	FoundationTotal int  `yaml:"foundation_total"`
}

type Attempt struct {
	AttemptNumber int                            `yaml:"attempt_number"`
	StartedAt     time.Time                      `yaml:"started_at"`
	CompletedAt   time.Time                      `yaml:"completed_at"`
	FixtureSet    FixtureSet                     `yaml:"fixture_set"`
	Precondition  PreconditionSnapshot           `yaml:"precondition"`
	Components    map[Component]ComponentOutcome `yaml:"components"`
	OverallPass   bool                           `yaml:"overall_pass"`
}

type Log struct {
	SchemaVersion int       `yaml:"schema_version"`
	CurriculumID  string    `yaml:"curriculum_id"`
	UpdatedAt     time.Time `yaml:"updated_at"`
	Attempts      []Attempt `yaml:"attempts"`
}

type Sidecar struct {
	SchemaVersion int                            `yaml:"schema_version"`
	CurriculumID  string                         `yaml:"curriculum_id"`
	AttemptNumber int                            `yaml:"attempt_number"`
	StartedAt     time.Time                      `yaml:"started_at"`
	FixtureSet    FixtureSet                     `yaml:"fixture_set"`
	Precondition  PreconditionSnapshot           `yaml:"precondition"`
	Components    map[Component]ComponentOutcome `yaml:"components"`
}

func (l *Log) Validate() error {
	if l.SchemaVersion > CurrentSchemaVersion {
		return fmt.Errorf("gate: log schema v%d newer than supported v%d", l.SchemaVersion, CurrentSchemaVersion)
	}
	return nil
}

func (s *Sidecar) Validate() error {
	if s.SchemaVersion > CurrentSchemaVersion {
		return fmt.Errorf("gate: sidecar schema v%d newer than supported v%d", s.SchemaVersion, CurrentSchemaVersion)
	}
	return nil
}
