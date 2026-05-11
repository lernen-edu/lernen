// Package forge orchestrates the forge pipeline. M3a shipped Stage 0
// (goal elicitation), M3b shipped Stage 1 (calibration), M3c added
// Stage 2 (recommendation), M3d adds Stage 3 (source ingestion).
// M3e adds Stage 4 (per-chapter scaffolding). Later sub-projects add
// their stages as siblings under internal/forge/.
package forge

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lernen-edu/lernen/internal/backends"
	"github.com/lernen-edu/lernen/internal/forge/calibration"
	"github.com/lernen-edu/lernen/internal/forge/goals"
	"github.com/lernen-edu/lernen/internal/forge/ingestion"
	"github.com/lernen-edu/lernen/internal/forge/profile"
	"github.com/lernen-edu/lernen/internal/forge/recommendation"
	"github.com/lernen-edu/lernen/internal/forge/reflection"
	"github.com/lernen-edu/lernen/internal/forge/scaffold"
	"github.com/lernen-edu/lernen/internal/languages"
	"github.com/lernen-edu/lernen/internal/tui"
)

// Options configures Run.
//
// Backend, ProfileDir, Stage0Run, Stage1Run, Stage2Run, Stage3Run,
// Stage4Pass1Run, Stage4Pass2Run, and SessionRunner are required.
// Stage0Run / Stage1Run / Stage2Run / Stage3Run are dispatchers —
// production wires goals.Run / calibration.Run / recommendation.Run /
// ingestion.Run; tests inject stubs. Stage4Pass1Run and Stage4Pass2Run
// dispatch scaffold.RunPass1 and scaffold.RunPass2 respectively.
//
// DevStage, when non-empty, bypasses resume detection and runs only
// the named stage. Stage prerequisites still apply:
// --stage=calibration requires goals.yaml; --stage=recommendation
// requires both goals.yaml and starting_point.yaml;
// --stage=ingestion requires goals.yaml, starting_point.yaml, and
// recommendation.yaml; --stage=scaffolding requires all four prior
// YAMLs. The orchestrator loads them before dispatch.
// M3e recognizes "goals", "calibration", "recommendation",
// "ingestion", and "scaffolding"; later sub-projects extend the set.
//
// Out is the writer for non-TUI status output (the resume message and
// per-stage success lines); defaults to os.Stdout.
type Options struct {
	Backend        backends.Backend
	ProfileDir     string
	Stage0Run      func(ctx context.Context, opts goals.Options) error
	Stage1Run      func(ctx context.Context, opts calibration.Options) error
	Stage2Run      func(ctx context.Context, opts recommendation.Options) error
	Stage3Run      func(ctx context.Context, opts ingestion.Options) error
	Stage4Pass1Run func(ctx context.Context, opts scaffold.Pass1Options) error
	Stage4Pass2Run func(ctx context.Context, opts scaffold.Pass2Options) error
	Stage5Run      func(ctx context.Context, opts reflection.Options) error
	Finalize       func(profileDir, manifestRoot string, r *reflection.ReflectionResult, forgeVersion, authoredBy string) (string, error)
	ManifestRoot   string
	ForgeVersion   string
	AuthoredBy     string
	SessionRunner  func(opts tui.Options) error
	ModelLabel     string
	DevStage       string

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
	if opts.Stage4Pass1Run == nil {
		return fmt.Errorf("forge: Options.Stage4Pass1Run is nil")
	}
	if opts.Stage4Pass2Run == nil {
		return fmt.Errorf("forge: Options.Stage4Pass2Run is nil")
	}
	if opts.Stage5Run == nil {
		return fmt.Errorf("forge: Options.Stage5Run is nil")
	}
	if opts.Finalize == nil {
		return fmt.Errorf("forge: Options.Finalize is nil")
	}
	if opts.ManifestRoot == "" {
		return fmt.Errorf("forge: Options.ManifestRoot is empty")
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
	cc, err := profile.LoadClassifiedChapters(opts.ProfileDir)
	if err != nil {
		return fmt.Errorf("forge: load classified chapters: %w", err)
	}
	if cc == nil {
		return dispatchPass1(ctx, opts, g, sp, rec, ing)
	}
	scaffolded, err := profile.ListChapterScaffolds(opts.ProfileDir)
	if err != nil {
		return fmt.Errorf("forge: list chapter scaffolds: %w", err)
	}
	allDone := true
	for _, cl := range cc.Classifications {
		if !scaffolded[cl.ChapterID] {
			allDone = false
			break
		}
	}
	if !allDone {
		return dispatchPass2(ctx, opts, g, sp, rec, ing, cc)
	}
	refl, err := profile.LoadReflection(opts.ProfileDir)
	if err != nil {
		return fmt.Errorf("forge: load reflection: %w", err)
	}
	if refl == nil {
		return dispatchReflection(ctx, opts, g, sp, rec, ing, cc)
	}
	manifestPath := filepath.Join(opts.ManifestRoot, refl.Curriculum.ID)
	if _, err := os.Stat(manifestPath); err != nil {
		if os.IsNotExist(err) {
			return runFinalizeOnly(opts, refl, out)
		}
		return fmt.Errorf("forge: stat manifest dir: %w", err)
	}
	fmt.Fprintf(out, "Forge complete; manifest at %s.\n", manifestPath)
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
	case "scaffolding":
		g, err := profile.LoadGoals(opts.ProfileDir)
		if err != nil || g == nil {
			return fmt.Errorf("forge: --stage=scaffolding requires goals.yaml; run Stage 0 first")
		}
		sp, err := profile.LoadStartingPoint(opts.ProfileDir)
		if err != nil || sp == nil {
			return fmt.Errorf("forge: --stage=scaffolding requires starting_point.yaml; run Stage 1 first")
		}
		rec, err := profile.LoadRecommendation(opts.ProfileDir)
		if err != nil || rec == nil {
			return fmt.Errorf("forge: --stage=scaffolding requires recommendation.yaml; run Stage 2 first")
		}
		ing, err := profile.LoadIngestion(opts.ProfileDir)
		if err != nil || ing == nil {
			return fmt.Errorf("forge: --stage=scaffolding requires ingestion.yaml; run Stage 3 first")
		}
		cc, err := profile.LoadClassifiedChapters(opts.ProfileDir)
		if err != nil {
			return fmt.Errorf("forge: --stage=scaffolding load classification: %w", err)
		}
		if cc == nil {
			return dispatchPass1(ctx, opts, g, sp, rec, ing)
		}
		return dispatchPass2(ctx, opts, g, sp, rec, ing, cc)
	case "reflection":
		g, err := profile.LoadGoals(opts.ProfileDir)
		if err != nil || g == nil {
			return fmt.Errorf("forge: --stage=reflection requires goals.yaml")
		}
		sp, err := profile.LoadStartingPoint(opts.ProfileDir)
		if err != nil || sp == nil {
			return fmt.Errorf("forge: --stage=reflection requires starting_point.yaml")
		}
		rec, err := profile.LoadRecommendation(opts.ProfileDir)
		if err != nil || rec == nil {
			return fmt.Errorf("forge: --stage=reflection requires recommendation.yaml")
		}
		ing, err := profile.LoadIngestion(opts.ProfileDir)
		if err != nil || ing == nil {
			return fmt.Errorf("forge: --stage=reflection requires ingestion.yaml")
		}
		cc, err := profile.LoadClassifiedChapters(opts.ProfileDir)
		if err != nil || cc == nil {
			return fmt.Errorf("forge: --stage=reflection requires classified_chapters.yaml")
		}
		scaffolded, err := profile.ListChapterScaffolds(opts.ProfileDir)
		if err != nil {
			return fmt.Errorf("forge: --stage=reflection: list scaffolds: %w", err)
		}
		for _, cl := range cc.Classifications {
			if !scaffolded[cl.ChapterID] {
				return fmt.Errorf("forge: --stage=reflection requires all chapters scaffolded; missing %s", cl.ChapterID)
			}
		}
		return dispatchReflection(ctx, opts, g, sp, rec, ing, cc)
	default:
		return fmt.Errorf("forge: unknown stage %q (supports: goals, calibration, recommendation, ingestion, scaffolding, reflection)", stage)
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
//
// "scaffolding" and "scaffolding-pass2" are handled specially: they are
// not bare stageFilenames keys (which use file basenames like
// "classified_chapters"), so they intercept before the generic
// BackupFromStage call and perform their own backup logic.
func runResetStage(ctx context.Context, opts Options, out io.Writer) error {
	now := time.Now().UTC()

	switch opts.ResetStage {
	case "scaffolding":
		// Back up classified_chapters.yaml + manifest_competencies.yaml +
		// chapter_scaffolds/ — everything from classified_chapters onwards
		// in the pipeline. BackupFromStage("classified_chapters", ...) covers
		// all three because classified_chapters is the first stageFilenames
		// entry at that tier and stageDirs are always swept.
		backed, err := profile.BackupFromStage(opts.ProfileDir, "classified_chapters", now)
		if len(backed) > 0 {
			fmt.Fprintf(out, "Backed up scaffolding outputs to %s: %s\n",
				now.Format("2006-01-02T15:04:05"), strings.Join(backed, ", "))
		}
		if err != nil {
			return fmt.Errorf("forge: --reset-stage=scaffolding (recover via --restore=%s): %w",
				now.Format("2006-01-02T15:04:05"), err)
		}
		if len(backed) == 0 {
			fmt.Fprintln(out, "No scaffolding outputs to back up; running scaffolding fresh.")
		}
		return dispatchByStageBasename(ctx, opts, opts.ResetStage)

	case "scaffolding-pass2":
		// Preserve classified_chapters.yaml; back up only
		// manifest_competencies.yaml and chapter_scaffolds/.
		ts := profile.FormatBackupTimestamp(now)
		mcLive := profile.ManifestCompetenciesPath(opts.ProfileDir)
		if _, statErr := os.Stat(mcLive); statErr == nil {
			dst := mcLive + "." + ts + ".bak"
			if err := os.Rename(mcLive, dst); err != nil {
				return fmt.Errorf("forge: --reset-stage=scaffolding-pass2 backup manifest_competencies (recover via --restore=%s): %w",
					now.Format("2006-01-02T15:04:05"), err)
			}
			fmt.Fprintf(out, "Backed up: %s\n", dst)
		} else if !os.IsNotExist(statErr) {
			return fmt.Errorf("forge: --reset-stage=scaffolding-pass2 stat manifest_competencies: %w", statErr)
		}
		scaffDirLive := profile.ChapterScaffoldsDir(opts.ProfileDir)
		if info, statErr := os.Stat(scaffDirLive); statErr == nil && info.IsDir() {
			dst := scaffDirLive + "." + ts + ".bak"
			if err := os.Rename(scaffDirLive, dst); err != nil {
				return fmt.Errorf("forge: --reset-stage=scaffolding-pass2 backup chapter_scaffolds (recover via --restore=%s): %w",
					now.Format("2006-01-02T15:04:05"), err)
			}
			fmt.Fprintf(out, "Backed up: %s\n", dst)
		} else if statErr != nil && !os.IsNotExist(statErr) {
			return fmt.Errorf("forge: --reset-stage=scaffolding-pass2 stat chapter_scaffolds: %w", statErr)
		}
		return dispatchByStageBasename(ctx, opts, opts.ResetStage)
	}

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
	case "scaffolding":
		// --reset-stage=scaffolding backs up all three M3e outputs, then
		// re-runs from Pass 1 (classification). Requires all four prior
		// YAMLs exactly as --stage=scaffolding does.
		g, err := profile.LoadGoals(opts.ProfileDir)
		if err != nil || g == nil {
			return fmt.Errorf("forge: --reset-stage=scaffolding requires goals.yaml")
		}
		sp, err := profile.LoadStartingPoint(opts.ProfileDir)
		if err != nil || sp == nil {
			return fmt.Errorf("forge: --reset-stage=scaffolding requires starting_point.yaml")
		}
		rec, err := profile.LoadRecommendation(opts.ProfileDir)
		if err != nil || rec == nil {
			return fmt.Errorf("forge: --reset-stage=scaffolding requires recommendation.yaml")
		}
		ing, err := profile.LoadIngestion(opts.ProfileDir)
		if err != nil || ing == nil {
			return fmt.Errorf("forge: --reset-stage=scaffolding requires ingestion.yaml")
		}
		return dispatchPass1(ctx, opts, g, sp, rec, ing)
	case "scaffolding-pass2":
		// --reset-stage=scaffolding-pass2 preserves classified_chapters.yaml
		// and re-runs Pass 2 (chapter scaffolding). Requires all four prior
		// YAMLs plus classified_chapters.yaml.
		g, err := profile.LoadGoals(opts.ProfileDir)
		if err != nil || g == nil {
			return fmt.Errorf("forge: --reset-stage=scaffolding-pass2 requires goals.yaml")
		}
		sp, err := profile.LoadStartingPoint(opts.ProfileDir)
		if err != nil || sp == nil {
			return fmt.Errorf("forge: --reset-stage=scaffolding-pass2 requires starting_point.yaml")
		}
		rec, err := profile.LoadRecommendation(opts.ProfileDir)
		if err != nil || rec == nil {
			return fmt.Errorf("forge: --reset-stage=scaffolding-pass2 requires recommendation.yaml")
		}
		ing, err := profile.LoadIngestion(opts.ProfileDir)
		if err != nil || ing == nil {
			return fmt.Errorf("forge: --reset-stage=scaffolding-pass2 requires ingestion.yaml")
		}
		cc, err := profile.LoadClassifiedChapters(opts.ProfileDir)
		if err != nil {
			return fmt.Errorf("forge: --reset-stage=scaffolding-pass2 load classified_chapters: %w", err)
		}
		if cc == nil {
			return fmt.Errorf("forge: --reset-stage=scaffolding-pass2 requires classified_chapters.yaml; run --reset-stage=scaffolding first")
		}
		return dispatchPass2(ctx, opts, g, sp, rec, ing, cc)
	case "reflection":
		g, err := profile.LoadGoals(opts.ProfileDir)
		if err != nil || g == nil {
			return fmt.Errorf("forge: --reset-stage=reflection requires goals.yaml")
		}
		sp, err := profile.LoadStartingPoint(opts.ProfileDir)
		if err != nil || sp == nil {
			return fmt.Errorf("forge: --reset-stage=reflection requires starting_point.yaml")
		}
		rec, err := profile.LoadRecommendation(opts.ProfileDir)
		if err != nil || rec == nil {
			return fmt.Errorf("forge: --reset-stage=reflection requires recommendation.yaml")
		}
		ing, err := profile.LoadIngestion(opts.ProfileDir)
		if err != nil || ing == nil {
			return fmt.Errorf("forge: --reset-stage=reflection requires ingestion.yaml")
		}
		cc, err := profile.LoadClassifiedChapters(opts.ProfileDir)
		if err != nil || cc == nil {
			return fmt.Errorf("forge: --reset-stage=reflection requires classified_chapters.yaml")
		}
		return dispatchReflection(ctx, opts, g, sp, rec, ing, cc)
	default:
		return fmt.Errorf("forge: --reset-stage: unknown stage %q (supported: goals, starting_point, recommendation, ingestion, scaffolding, scaffolding-pass2, reflection)", basename)
	}
}

// dispatchPass1 is the only path to opts.Stage4Pass1Run. All call sites
// (resume detector, runStage("scaffolding")) load the four prior YAMLs
// first and pass non-nil pointers — scaffold.RunPass1 does not load any
// of them itself.
func dispatchPass1(ctx context.Context, opts Options, g *goals.Goals, sp *calibration.StartingPoint, rec *recommendation.Recommendation, ing *ingestion.Ingestion) error {
	return opts.Stage4Pass1Run(ctx, scaffold.Pass1Options{
		Backend:                opts.Backend,
		SessionRunner:          opts.SessionRunner,
		ProfileDir:             opts.ProfileDir,
		Goals:                  g,
		StartingPoint:          sp,
		Recommendation:         rec,
		Ingestion:              ing,
		SaveClassifiedChapters: profile.SaveClassifiedChapters,
		ClassifiedChaptersPath: profile.ClassifiedChaptersPath,
		ModelLabel:             opts.ModelLabel,
		Out:                    opts.Out,
	})
}

// dispatchPass2 is the only path to opts.Stage4Pass2Run. Loads the four
// prior YAMLs PLUS classified_chapters.yaml as preconditions.
func dispatchPass2(ctx context.Context, opts Options, g *goals.Goals, sp *calibration.StartingPoint, rec *recommendation.Recommendation, ing *ingestion.Ingestion, cc *scaffold.ClassifiedChapters) error {
	return opts.Stage4Pass2Run(ctx, scaffold.Pass2Options{
		Backend:              opts.Backend,
		SessionRunner:        opts.SessionRunner,
		ProfileDir:           opts.ProfileDir,
		Goals:                g,
		StartingPoint:        sp,
		Recommendation:       rec,
		Ingestion:            ing,
		ClassifiedChapters:   cc,
		ChapterScaffoldsDir:  profile.ChapterScaffoldsDir,
		SaveChapterScaffold:  profile.SaveChapterScaffold,
		AppendCompetencies:   profile.AppendCompetencies,
		ListChapterScaffolds: profile.ListChapterScaffolds,
		ModelLabel:           opts.ModelLabel,
		Out:                  opts.Out,
	})
}

// dispatchReflection is the only path to opts.Stage5Run. All call
// sites (resume detector, runStage("reflection"),
// dispatchByStageBasename("reflection")) load all six prior artifacts
// and pass non-nil pointers. reflection.RunReflection does not load
// any of them itself.
func dispatchReflection(ctx context.Context, opts Options, g *goals.Goals, sp *calibration.StartingPoint, rec *recommendation.Recommendation, ing *ingestion.Ingestion, cc *scaffold.ClassifiedChapters) error {
	mc, err := profile.LoadManifestCompetencies(opts.ProfileDir)
	if err != nil {
		return fmt.Errorf("forge: load manifest_competencies: %w", err)
	}
	scaffolds, err := loadOrderedScaffolds(opts.ProfileDir, cc)
	if err != nil {
		return fmt.Errorf("forge: load chapter scaffolds: %w", err)
	}
	var comps []scaffold.Competency
	if mc != nil {
		comps = mc.Competencies
	}
	return opts.Stage5Run(ctx, reflection.Options{
		Backend:            opts.Backend,
		SessionRunner:      opts.SessionRunner,
		ProfileDir:         opts.ProfileDir,
		ManifestRoot:       opts.ManifestRoot,
		Goals:              g,
		StartingPoint:      sp,
		Recommendation:     rec,
		Ingestion:          ing,
		ClassifiedChapters: cc,
		Competencies:       comps,
		Scaffolds:          scaffolds,
		SaveReflection:     profile.SaveReflection,
		Finalize:           opts.Finalize,
		ForgeVersion:       opts.ForgeVersion,
		AuthoredBy:         opts.AuthoredBy,
		ModelLabel:         opts.ModelLabel,
		Out:                opts.Out,
	})
}

// runFinalizeOnly re-invokes Finalize after a prior reflection.yaml
// save without opening the TUI. Idempotent: covers (a) prior finalize
// failed mid-write, (b) user manually deleted the manifest dir.
func runFinalizeOnly(opts Options, refl *reflection.ReflectionResult, out io.Writer) error {
	fmt.Fprintf(out, "Re-running finalize for `%s`…\n", refl.Curriculum.ID)
	path, err := opts.Finalize(opts.ProfileDir, opts.ManifestRoot, refl, opts.ForgeVersion, opts.AuthoredBy)
	if err != nil {
		return fmt.Errorf("forge: finalize-only: %w", err)
	}
	fmt.Fprintf(out, "Manifest published at %s.\n", path)
	return nil
}

// loadOrderedScaffolds loads chapter_scaffolds in classified order
// (skipping any that are missing — those were /skip-chapter'd in Pass 2
// and may not be on disk).
func loadOrderedScaffolds(profileDir string, cc *scaffold.ClassifiedChapters) ([]scaffold.ChapterScaffold, error) {
	out := make([]scaffold.ChapterScaffold, 0, len(cc.Classifications))
	for _, cl := range cc.Classifications {
		s, err := profile.LoadChapterScaffold(profileDir, cl.ChapterID)
		if err != nil {
			return nil, fmt.Errorf("load scaffold %s: %w", cl.ChapterID, err)
		}
		if s == nil {
			continue
		}
		out = append(out, *s)
	}
	return out, nil
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
