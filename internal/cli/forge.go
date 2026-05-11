package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"

	"github.com/lernen-edu/lernen/internal/backends"
	"github.com/lernen-edu/lernen/internal/config"
	"github.com/lernen-edu/lernen/internal/forge"
	"github.com/lernen-edu/lernen/internal/forge/calibration"
	"github.com/lernen-edu/lernen/internal/forge/goals"
	"github.com/lernen-edu/lernen/internal/forge/ingestion"
	"github.com/lernen-edu/lernen/internal/forge/profile"
	"github.com/lernen-edu/lernen/internal/forge/recommendation"
	"github.com/lernen-edu/lernen/internal/forge/reflection"
	"github.com/lernen-edu/lernen/internal/forge/scaffold"
	"github.com/lernen-edu/lernen/internal/tui"
)

// forgeVersion is the version string written to curriculum.yaml as
// forge_version. Hardcoded for the initial M3f wiring; a followup will
// derive it from internal/version once that helper exists.
const forgeVersion = "v0.1.0"

// ForgeDeps is the dependency-injection surface for `lernen forge`. The
// production NewForgeCmd uses ProductionForgeDeps; tests pass mocks so
// the command runs without spawning a real Bubble Tea program.
type ForgeDeps struct {
	// BackendFactory constructs the backend selected by the loaded
	// config. Production wires productionBackend; tests typically
	// return *fake.FakeBackend regardless of cfg.Backend.
	BackendFactory func(*config.Config) (backends.Backend, error)

	// SessionRunner runs the actual TUI program. Production wires
	// productionForgeSessionRunner (which calls tea.NewProgram(...).Run()).
	// Tests typically capture opts and return nil so the test binary
	// doesn't try to take over the terminal.
	SessionRunner func(tui.Options) error

	// ConfigPath optionally overrides the default ConfigFile() lookup.
	// Tests usually point this at a path that does not exist so
	// config.Load returns Default() instead of reading the user's
	// real config file.
	ConfigPath string

	// ForgeRunner runs the orchestrator. Production wires forge.Run;
	// tests typically capture opts and return nil so the orchestrator
	// doesn't actually try to dispatch a stage.
	ForgeRunner func(ctx context.Context, opts forge.Options) error
}

// ProductionForgeDeps returns deps wired for the shipped binary.
func ProductionForgeDeps() ForgeDeps {
	return ForgeDeps{
		BackendFactory: productionBackend,
		SessionRunner:  productionForgeSessionRunner,
		ForgeRunner:    forge.Run,
	}
}

// NewForgeCmd builds the `lernen forge` Cobra command. M3a + M3b + M3c
// ship Stage 0 (goal elicitation), Stage 1 (calibration), and Stage 2
// (recommendation); later sub-projects extend forge.Run's dispatch
// internally — the command surface stays a single resumable
// invocation.
func NewForgeCmd(deps ForgeDeps) *cobra.Command {
	var (
		devStage        string
		resetFlag       bool
		restoreFlag     string
		listBackups     bool
		resetStageFlag  string
	)
	cmd := &cobra.Command{
		Use:   "forge",
		Short: "Author a custom curriculum (resumable; supports --reset, --restore, --list-backups, --reset-stage)",
		Long: `Author a custom curriculum manifest through a guided dialogue.

The forge runs in stages — goal elicitation, calibration, recommendation,
source ingestion, per-chapter scaffolding, and reflection. Each stage
produces output files in your profile directory; running the command
again resumes from the next incomplete stage.

Stage 4 (scaffolding) runs in two passes. Pass 1 walks the chapter list
from ingestion.yaml and tags each chapter orientation or content. Pass 2
iterates unscaffolded chapters in a single TUI session and produces a
chapter file per chapter under profile/chapter_scaffolds/.

Stage 5 (reflection) walks you through what you authored and writes the
runtime-shaped manifest to <data>/manifests/<curriculum-id>/ — at which
point you can run "lernen work <curriculum-id>" to start Phase 1.

Flags:
      --reset                    back up current session and start fresh from Stage 0
      --reset-stage=<name>       back up <name> and downstream stages, then re-run from <name>
                                 (name is a stage basename: goals, starting_point, recommendation,
                                 ingestion, scaffolding, scaffolding-pass2, reflection)
      --restore=<timestamp>      revert to backup at <timestamp> (e.g. 2026-05-09T14:30:00)
      --list-backups             list available backups, newest-first
  -h, --help                     help for forge`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			set := 0
			if resetFlag {
				set++
			}
			if restoreFlag != "" {
				set++
			}
			if listBackups {
				set++
			}
			if devStage != "" {
				set++
			}
			if resetStageFlag != "" {
				set++
			}
			if set > 1 {
				return errors.New("forge: --reset, --reset-stage, --restore, --list-backups, and --stage are mutually exclusive")
			}
			if restoreFlag != "" {
				if _, err := profile.ParseDisplayTimestamp(restoreFlag); err != nil {
					return fmt.Errorf("forge: --restore: %w", err)
				}
			}
			return runForge(cmd.Context(), forgeArgs{
				devStage:    devStage,
				reset:       resetFlag,
				restore:     restoreFlag,
				listBackups: listBackups,
				resetStage:  resetStageFlag,
			}, deps)
		},
	}
	cmd.Flags().StringVar(&devStage, "stage", "", "")
	if err := cmd.Flags().MarkHidden("stage"); err != nil {
		panic(fmt.Sprintf("forge: hide --stage flag: %v", err))
	}
	cmd.Flags().BoolVar(&resetFlag, "reset", false,
		"back up current session and start fresh from Stage 0")
	// Backticks tell pflag's UnquoteUsage to display "<timestamp>" as the
	// flag's value name in --help (instead of the generic "string").
	cmd.Flags().StringVar(&restoreFlag, "restore", "",
		"revert to backup at `<timestamp>` (e.g. 2026-05-09T14:30:00)")
	cmd.Flags().BoolVar(&listBackups, "list-backups", false,
		"list available backups, newest-first")
	// Same backtick trick: "<name>" displays as the flag's value name.
	cmd.Flags().StringVar(&resetStageFlag, "reset-stage", "",
		"back up `<name>` and downstream stages, then re-run from <name> (goals, starting_point, recommendation, ingestion, scaffolding, scaffolding-pass2, reflection)")
	// main() prints the returned error; suppress cobra's duplicate.
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	// Omit Cobra's auto-generated "Flags:" section since the Long block
	// already lists every flag in the canonical = form. pflag hardcodes
	// space as the separator between flag name and value name (e.g.
	// "--restore <timestamp>"), which would contradict the Long block's
	// "--restore=<timestamp>" listing. Custom template below is Cobra's
	// default with the {{if .HasAvailableLocalFlags}}...{{end}} block
	// removed.
	cmd.SetUsageTemplate(`Usage:{{if .Runnable}}
  {{.UseLine}}{{end}}{{if gt (len .Aliases) 0}}

Aliases:
  {{.NameAndAliases}}{{end}}{{if .HasExample}}

Examples:
{{.Example}}{{end}}{{if .HasAvailableInheritedFlags}}

Global Flags:
{{.InheritedFlags.FlagUsages | trimTrailingWhitespaces}}{{end}}
`)
	return cmd
}

type forgeArgs struct {
	devStage    string
	reset       bool
	restore     string
	listBackups bool
	resetStage  string
}

func runForge(ctx context.Context, args forgeArgs, deps ForgeDeps) error {
	if deps.BackendFactory == nil {
		return errors.New("forge: BackendFactory is nil (programmer error)")
	}
	if deps.SessionRunner == nil {
		return errors.New("forge: SessionRunner is nil (programmer error)")
	}
	if deps.ForgeRunner == nil {
		return errors.New("forge: ForgeRunner is nil (programmer error)")
	}

	configPath := deps.ConfigPath
	if configPath == "" {
		var err error
		configPath, err = ConfigFile()
		if err != nil {
			return fmt.Errorf("forge: resolve config path: %w", err)
		}
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("forge: load config: %w", err)
	}

	backend, err := deps.BackendFactory(&cfg)
	if err != nil {
		return fmt.Errorf("forge: construct backend: %w", err)
	}
	if err := backend.HealthCheck(ctx); err != nil {
		return fmt.Errorf("forge: backend health check failed (%s): %w", backend.Name(), err)
	}

	profileDir, err := ProfileDir()
	if err != nil {
		return fmt.Errorf("forge: resolve profile dir: %w", err)
	}

	manifestRoot, err := ManifestsDir()
	if err != nil {
		return fmt.Errorf("forge: resolve manifests dir: %w", err)
	}

	opts := forge.Options{
		Backend:        backend,
		ProfileDir:     profileDir,
		Stage0Run:      goals.Run,
		Stage1Run:      calibration.Run,
		Stage2Run:      recommendation.Run,
		Stage3Run:      ingestion.Run,
		Stage4Pass1Run: scaffold.RunPass1,
		Stage4Pass2Run: scaffold.RunPass2,
		Stage5Run:      reflection.RunReflection,
		Finalize:       reflection.Finalize,
		SessionRunner:  deps.SessionRunner,
		ModelLabel:     modelLabel(&cfg),
		ManifestRoot:   manifestRoot,
		ForgeVersion:   forgeVersion,
		AuthoredBy:     resolveAuthoredBy(),
		DevStage:       args.devStage,
		Reset:          args.reset,
		Restore:        args.restore,
		ListBackups:    args.listBackups,
		ResetStage:     args.resetStage,
	}
	return deps.ForgeRunner(ctx, opts)
}

// resolveAuthoredBy returns the value used for curriculum.yaml's
// author_attribution and authored_by fields. Tries git config user.name
// first; falls back to $USER; falls back to "lernen-forge".
func resolveAuthoredBy() string {
	out, err := exec.Command("git", "config", "user.name").Output()
	if err == nil {
		name := strings.TrimSpace(string(out))
		if name != "" {
			return name
		}
	}
	if u := os.Getenv("USER"); u != "" {
		return u
	}
	return "lernen-forge"
}

// productionForgeSessionRunner runs the actual Bubble Tea program for
// the forge stages. Mirrors productionSessionRunner from work.go:
// inline rendering (no altscreen), no program-level mouse capture, and
// a one-time terminal clear (ANSI ESC[2J + cursor-home) before the
// program runs so prior shell activity doesn't bleed into the first
// paint.
func productionForgeSessionRunner(opts tui.Options) error {
	fmt.Print("\x1b[2J\x1b[H")
	p := tea.NewProgram(tui.New(opts))
	_, err := p.Run()
	return err
}
