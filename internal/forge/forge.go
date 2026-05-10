// Package forge orchestrates the forge pipeline. M3a shipped Stage 0
// (goal elicitation), M3b shipped Stage 1 (calibration), M3c added
// Stage 2 (recommendation), M3d adds Stage 3 (source ingestion).
// Later sub-projects add their stages as siblings under internal/forge/.
package forge

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/lernen-edu/lernen/internal/backends"
	"github.com/lernen-edu/lernen/internal/forge/calibration"
	"github.com/lernen-edu/lernen/internal/forge/goals"
	"github.com/lernen-edu/lernen/internal/forge/ingestion"
	"github.com/lernen-edu/lernen/internal/forge/profile"
	"github.com/lernen-edu/lernen/internal/forge/recommendation"
	"github.com/lernen-edu/lernen/internal/languages"
	"github.com/lernen-edu/lernen/internal/tui"
)

// Options configures Run.
//
// Backend, ProfileDir, Stage0Run, Stage1Run, Stage2Run, Stage3Run, and
// SessionRunner are required. Stage0Run / Stage1Run / Stage2Run /
// Stage3Run are dispatchers — production wires goals.Run /
// calibration.Run / recommendation.Run / ingestion.Run; tests inject
// stubs.
//
// DevStage, when non-empty, bypasses resume detection and runs only
// the named stage. Stage prerequisites still apply:
// --stage=calibration requires goals.yaml; --stage=recommendation
// requires both goals.yaml and starting_point.yaml;
// --stage=ingestion requires goals.yaml, starting_point.yaml, and
// recommendation.yaml. The orchestrator loads them before dispatch.
// M3d recognizes "goals", "calibration", "recommendation", and
// "ingestion"; later sub-projects extend the recognized set.
//
// Out is the writer for non-TUI status output (the resume message and
// per-stage success lines); defaults to os.Stdout.
type Options struct {
	Backend       backends.Backend
	ProfileDir    string
	Stage0Run     func(ctx context.Context, opts goals.Options) error
	Stage1Run     func(ctx context.Context, opts calibration.Options) error
	Stage2Run     func(ctx context.Context, opts recommendation.Options) error
	Stage3Run     func(ctx context.Context, opts ingestion.Options) error
	SessionRunner func(opts tui.Options) error
	ModelLabel    string
	DevStage      string

	// Reset, Restore, and ListBackups are mutually exclusive with each
	// other and with DevStage. Setting more than one is enforced as an
	// error at the CLI boundary; Run trusts the caller. Each is
	// dispatched ahead of the resume detector.
	//
	// Reset: back up any existing stage YAMLs to timestamped .bak
	// siblings (one shared timestamp), then dispatch Stage 0 fresh.
	Reset bool

	// Restore: parse Restore as a timestamp; back up current state to
	// a fresh (now) sibling set; promote the .bak set at the parsed
	// timestamp to live; do not dispatch any stage.
	Restore string

	// ListBackups: walk the profile dir, group .bak files by
	// timestamp, print sorted newest-first to Out, exit without
	// dispatching.
	ListBackups bool

	// ResetStage: name of a stage (file basename without .yaml — e.g.,
	// "starting_point" or "recommendation") to back up along with every
	// downstream stage. After backup, the named stage is dispatched
	// fresh. Upstream YAMLs remain live so the user keeps the work
	// they're not redoing. Mutually exclusive with Reset, Restore,
	// ListBackups, and DevStage.
	ResetStage string

	Out io.Writer
}

// Run executes the forge pipeline. Resume detection: dispatch Stage 0
// when goals.yaml is absent; dispatch Stage 1 when goals.yaml is
// present but starting_point.yaml is absent; dispatch Stage 2 when
// both are present but recommendation.yaml is absent; print the
// next-stage hint when all three are present.
//
// When DevStage is set, Run bypasses resume detection. Stage 1 and
// Stage 2 still require their prerequisites (orchestrator loads them).
func Run(ctx context.Context, opts Options) error {
	if opts.Backend == nil {
		return fmt.Errorf("forge: Options.Backend is nil")
	}
	if opts.ProfileDir == "" {
		return fmt.Errorf("forge: Options.ProfileDir is empty")
	}
	if opts.Stage0Run == nil {
		return fmt.Errorf("forge: Options.Stage0Run is nil")
	}
	if opts.Stage1Run == nil {
		return fmt.Errorf("forge: Options.Stage1Run is nil")
	}
	if opts.Stage2Run == nil {
		return fmt.Errorf("forge: Options.Stage2Run is nil")
	}
	if opts.Stage3Run == nil {
		return fmt.Errorf("forge: Options.Stage3Run is nil")
	}
	if opts.SessionRunner == nil {
		return fmt.Errorf("forge: Options.SessionRunner is nil")
	}

	out := opts.Out
	if out == nil {
		out = os.Stdout
	}

	if opts.ListBackups {
		return runListBackups(opts, out)
	}
	if opts.Restore != "" {
		return runRestore(opts, out)
	}
	if opts.Reset {
		return runReset(ctx, opts, out)
	}
	if opts.ResetStage != "" {
		return runResetStage(ctx, opts, out)
	}

	if opts.DevStage != "" {
		return runStage(ctx, opts, opts.DevStage)
	}

	g, err := profile.LoadGoals(opts.ProfileDir)
	if err != nil {
		return fmt.Errorf("forge: load goals: %w", err)
	}
	if g == nil {
		return dispatchGoals(ctx, opts)
	}
	sp, err := profile.LoadStartingPoint(opts.ProfileDir)
	if err != nil {
		return fmt.Errorf("forge: load starting point: %w", err)
	}
	if sp == nil {
		return dispatchCalibration(ctx, opts, g)
	}
	rec, err := profile.LoadRecommendation(opts.ProfileDir)
	if err != nil {
		return fmt.Errorf("forge: load recommendation: %w", err)
	}
	if rec == nil {
		return dispatchRecommendation(ctx, opts, g, sp)
	}
	ing, err := profile.LoadIngestion(opts.ProfileDir)
	if err != nil {
		return fmt.Errorf("forge: load ingestion: %w", err)
	}
	if ing == nil {
		return dispatchIngestion(ctx, opts, g, sp, rec)
	}
	fmt.Fprintf(out, "Stage 0 (goals) already complete at %s.\n", profile.GoalsPath(opts.ProfileDir))
	fmt.Fprintf(out, "Stage 1 (calibration) already complete at %s.\n", profile.StartingPointPath(opts.ProfileDir))
	fmt.Fprintf(out, "Stage 2 (recommendation) already complete at %s.\n", profile.RecommendationPath(opts.ProfileDir))
	fmt.Fprintf(out, "Stage 3 (ingestion) already complete at %s.\n", profile.IngestionPath(opts.ProfileDir))
	fmt.Fprintln(out, "Stage 0/1/2/3 complete; Stage 4 (per-chapter scaffolding) coming next.")
	return nil
}

func runStage(ctx context.Context, opts Options, stage string) error {
	switch stage {
	case "goals":
		return dispatchGoals(ctx, opts)
	case "calibration":
		g, err := profile.LoadGoals(opts.ProfileDir)
		if err != nil {
			return fmt.Errorf("forge: --stage=calibration requires goals.yaml: %w", err)
		}
		if g == nil {
			return fmt.Errorf("forge: --stage=calibration requires goals.yaml; run Stage 0 first")
		}
		return dispatchCalibration(ctx, opts, g)
	case "recommendation":
		g, err := profile.LoadGoals(opts.ProfileDir)
		if err != nil {
			return fmt.Errorf("forge: --stage=recommendation requires goals.yaml: %w", err)
		}
		if g == nil {
			return fmt.Errorf("forge: --stage=recommendation requires goals.yaml; run Stage 0 first")
		}
		sp, err := profile.LoadStartingPoint(opts.ProfileDir)
		if err != nil {
			return fmt.Errorf("forge: --stage=recommendation requires starting_point.yaml: %w", err)
		}
		if sp == nil {
			return fmt.Errorf("forge: --stage=recommendation requires starting_point.yaml; run Stage 1 first")
		}
		return dispatchRecommendation(ctx, opts, g, sp)
	case "ingestion":
		g, err := profile.LoadGoals(opts.ProfileDir)
		if err != nil {
			return fmt.Errorf("forge: --stage=ingestion requires goals.yaml: %w", err)
		}
		if g == nil {
			return fmt.Errorf("forge: --stage=ingestion requires goals.yaml; run Stage 0 first")
		}
		sp, err := profile.LoadStartingPoint(opts.ProfileDir)
		if err != nil {
			return fmt.Errorf("forge: --stage=ingestion requires starting_point.yaml: %w", err)
		}
		if sp == nil {
			return fmt.Errorf("forge: --stage=ingestion requires starting_point.yaml; run Stage 1 first")
		}
		rec, err := profile.LoadRecommendation(opts.ProfileDir)
		if err != nil {
			return fmt.Errorf("forge: --stage=ingestion requires recommendation.yaml: %w", err)
		}
		if rec == nil {
			return fmt.Errorf("forge: --stage=ingestion requires recommendation.yaml; run Stage 2 first")
		}
		return dispatchIngestion(ctx, opts, g, sp, rec)
	default:
		return fmt.Errorf("forge: unknown stage %q (supports: goals, calibration, recommendation, ingestion)", stage)
	}
}

func dispatchGoals(ctx context.Context, opts Options) error {
	return opts.Stage0Run(ctx, goals.Options{
		Backend:       opts.Backend,
		SessionRunner: opts.SessionRunner,
		ProfileDir:    opts.ProfileDir,
		SaveGoals:     profile.SaveGoals,
		GoalsPath:     profile.GoalsPath,
		ModelLabel:    opts.ModelLabel,
		Out:           opts.Out,
	})
}

// dispatchCalibration is the only path to opts.Stage1Run. Both call
// sites (resume detector and runStage("calibration")) load goals first
// and pass a non-nil g — calibration.Run does not load goals itself.
// This is the M3b §4.2 invariant.
func dispatchCalibration(ctx context.Context, opts Options, g *goals.Goals) error {
	return opts.Stage1Run(ctx, calibration.Options{
		Backend:           opts.Backend,
		SessionRunner:     opts.SessionRunner,
		ProfileDir:        opts.ProfileDir,
		Goals:             g,
		SaveStartingPoint: profile.SaveStartingPoint,
		StartingPointPath: profile.StartingPointPath,
		ModelLabel:        opts.ModelLabel,
		Out:               opts.Out,
	})
}

// dispatchRecommendation is the only path to opts.Stage2Run. Both call
// sites (resume detector and runStage("recommendation")) load BOTH
// goals AND starting_point first and pass non-nil pointers —
// recommendation.Run does not load either itself. This extends the M3b
// invariant pattern for two preconditions (M3c spec §4.2).
//
// The Adapters slice is built from the live LanguageAdapter registry
// here (rather than inside recommendation.Run) so the package stays
// testable in isolation: tests inject custom AdapterInfo sets without
// going through the global registry.
func dispatchRecommendation(ctx context.Context, opts Options, g *goals.Goals, sp *calibration.StartingPoint) error {
	return opts.Stage2Run(ctx, recommendation.Options{
		Backend:            opts.Backend,
		SessionRunner:      opts.SessionRunner,
		ProfileDir:         opts.ProfileDir,
		Goals:              g,
		StartingPoint:      sp,
		Adapters:           buildAdapterInfos(),
		SaveRecommendation: profile.SaveRecommendation,
		RecommendationPath: profile.RecommendationPath,
		ModelLabel:         opts.ModelLabel,
		Out:                opts.Out,
	})
}

// dispatchIngestion is the only path to opts.Stage3Run. All call sites
// (resume detector, runStage("ingestion"), and
// dispatchByStageBasename("ingestion")) load goals, starting_point,
// AND recommendation first and pass non-nil pointers —
// ingestion.Run does not load any of them itself. This extends the M3b
// invariant pattern for three preconditions (M3d spec §4.2).
func dispatchIngestion(ctx context.Context, opts Options, g *goals.Goals, sp *calibration.StartingPoint, rec *recommendation.Recommendation) error {
	return opts.Stage3Run(ctx, ingestion.Options{
		Backend:        opts.Backend,
		SessionRunner:  opts.SessionRunner,
		ProfileDir:     opts.ProfileDir,
		Goals:          g,
		StartingPoint:  sp,
		Recommendation: rec,
		SaveIngestion:  profile.SaveIngestion,
		IngestionPath:  profile.IngestionPath,
		ModelLabel:     opts.ModelLabel,
		Out:            opts.Out,
	})
}

func runReset(ctx context.Context, opts Options, out io.Writer) error {
	now := time.Now().UTC()
	backed, err := profile.BackupAll(opts.ProfileDir, now)
	// Print the partial list (or full success) BEFORE checking err so the
	// user sees what was saved even on mid-loop failure. Spec §6: caller
	// prints what was saved.
	if len(backed) > 0 {
		fmt.Fprintf(out, "Backed up prior session to %s: %s\n",
			now.Format("2006-01-02T15:04:05"), strings.Join(backed, ", "))
	}
	if err != nil {
		return fmt.Errorf("forge: --reset (recover via --restore=%s): %w",
			now.Format("2006-01-02T15:04:05"), err)
	}
	if len(backed) == 0 {
		fmt.Fprintln(out, "No prior session to back up; starting fresh.")
	}
	return dispatchGoals(ctx, opts)
}

func runRestore(opts Options, out io.Writer) error {
	ts, err := profile.ParseDisplayTimestamp(opts.Restore)
	if err != nil {
		return fmt.Errorf("forge: --restore: %w", err)
	}
	now := time.Now().UTC()
	if err := profile.Restore(opts.ProfileDir, ts, now); err != nil {
		return fmt.Errorf("forge: --restore: %w", err)
	}
	fmt.Fprintf(out, "Restored profile to %s. Prior state backed up to %s (use --restore=%s to undo).\n",
		ts.Format("2006-01-02T15:04:05"),
		now.Format("2006-01-02T15:04:05"),
		now.Format("2006-01-02T15:04:05"))
	return nil
}

func runListBackups(opts Options, out io.Writer) error {
	sets, err := profile.ListBackups(opts.ProfileDir)
	if err != nil {
		return fmt.Errorf("forge: --list-backups: %w", err)
	}
	if len(sets) == 0 {
		fmt.Fprintf(out, "No backups found in %s.\n", opts.ProfileDir)
		return nil
	}
	for _, set := range sets {
		fmt.Fprintf(out, "%s  %s\n", set.Timestamp.Format("2006-01-02T15:04:05"), strings.Join(set.Stages, " "))
	}
	fmt.Fprintln(out, "(use --restore=<timestamp> to revert)")
	return nil
}

// runResetStage backs up the named stage and every downstream stage to
// timestamped .bak siblings, then dispatches the named stage fresh.
// Upstream YAMLs remain live (the user keeps that work). Mirrors
// runReset's printing-and-error contract: the partial list is reported
// before any error so the user sees what was saved, and the error
// message embeds the recovery timestamp.
func runResetStage(ctx context.Context, opts Options, out io.Writer) error {
	now := time.Now().UTC()
	backed, err := profile.BackupFromStage(opts.ProfileDir, opts.ResetStage, now)
	if len(backed) > 0 {
		fmt.Fprintf(out, "Backed up %s and downstream to %s: %s\n",
			opts.ResetStage, now.Format("2006-01-02T15:04:05"), strings.Join(backed, ", "))
	}
	if err != nil {
		// Two cases: unknown stage (no mutation) or partial filesystem
		// failure mid-loop (some renames succeeded). The recovery hint
		// only helps in the second case but is harmless in the first.
		return fmt.Errorf("forge: --reset-stage (recover via --restore=%s): %w",
			now.Format("2006-01-02T15:04:05"), err)
	}
	if len(backed) == 0 {
		fmt.Fprintf(out, "No %s.yaml or downstream files to back up; running %s fresh.\n",
			opts.ResetStage, opts.ResetStage)
	}
	return dispatchByStageBasename(ctx, opts, opts.ResetStage)
}

// dispatchByStageBasename routes to the right Stage*Run based on a
// file-basename stage name (matching --list-backups output and the
// names in the user's profile dir). Loads upstream prerequisites the
// same way runStage does for DevStage.
//
// Note the namespace asymmetry with runStage: --stage uses the
// package-name "calibration" (a developer-only flag), while
// --reset-stage and --list-backups use the file-basename
// "starting_point" (the user-facing form). This helper bridges to the
// shared dispatch* functions.
func dispatchByStageBasename(ctx context.Context, opts Options, basename string) error {
	switch basename {
	case "goals":
		return dispatchGoals(ctx, opts)
	case "starting_point":
		g, err := profile.LoadGoals(opts.ProfileDir)
		if err != nil {
			return fmt.Errorf("forge: --reset-stage=starting_point requires goals.yaml: %w", err)
		}
		if g == nil {
			return fmt.Errorf("forge: --reset-stage=starting_point requires goals.yaml; run --reset-stage=goals first")
		}
		return dispatchCalibration(ctx, opts, g)
	case "recommendation":
		g, err := profile.LoadGoals(opts.ProfileDir)
		if err != nil {
			return fmt.Errorf("forge: --reset-stage=recommendation requires goals.yaml: %w", err)
		}
		if g == nil {
			return fmt.Errorf("forge: --reset-stage=recommendation requires goals.yaml; run --reset-stage=goals first")
		}
		sp, err := profile.LoadStartingPoint(opts.ProfileDir)
		if err != nil {
			return fmt.Errorf("forge: --reset-stage=recommendation requires starting_point.yaml: %w", err)
		}
		if sp == nil {
			return fmt.Errorf("forge: --reset-stage=recommendation requires starting_point.yaml; run --reset-stage=starting_point first")
		}
		return dispatchRecommendation(ctx, opts, g, sp)
	case "ingestion":
		g, err := profile.LoadGoals(opts.ProfileDir)
		if err != nil || g == nil {
			return fmt.Errorf("forge: --reset-stage=ingestion requires goals.yaml")
		}
		sp, err := profile.LoadStartingPoint(opts.ProfileDir)
		if err != nil || sp == nil {
			return fmt.Errorf("forge: --reset-stage=ingestion requires starting_point.yaml")
		}
		rec, err := profile.LoadRecommendation(opts.ProfileDir)
		if err != nil || rec == nil {
			return fmt.Errorf("forge: --reset-stage=ingestion requires recommendation.yaml")
		}
		return dispatchIngestion(ctx, opts, g, sp, rec)
	default:
		return fmt.Errorf("forge: --reset-stage: unknown stage %q (supported: goals, starting_point, recommendation, ingestion)", basename)
	}
}

// buildAdapterInfos walks the live LanguageAdapter registry and
// returns a sorted slice of {ID, DisplayName} DTOs suitable for the
// recommendation prompt. Empty if no adapters are registered (which
// causes recommendation.Run to error out — that's the right behavior:
// no adapters = nothing to recommend).
func buildAdapterInfos() []recommendation.AdapterInfo {
	ids := languages.IDs()
	out := make([]recommendation.AdapterInfo, 0, len(ids))
	for _, id := range ids {
		a, ok := languages.Get(id)
		if !ok {
			continue // unreachable in practice — IDs() returned an ID Get can't find
		}
		out = append(out, recommendation.AdapterInfo{
			ID:          a.ID(),
			DisplayName: a.DisplayName(),
		})
	}
	return out
}
