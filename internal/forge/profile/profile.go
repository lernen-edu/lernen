// Package profile is the user-profile I/O layer for forge. It owns the
// read/write helpers for the per-stage output files (goals.yaml from
// Stage 0; starting_point.yaml from Stage 1; recommendation.yaml from
// Stage 2; ingestion.yaml from Stage 3; later sub-projects add more
// siblings). Atomic writes ensure a crash mid-write never leaves a
// half-file at the final path — the forge orchestrator's resume
// detector relies on existence-implies-valid.
//
// Path-agnostic: the profile directory is supplied by the caller. Use
// internal/cli.ProfileDir() at the CLI boundary (cli/forge.go) to
// resolve the production path; tests pass t.TempDir() directly. This
// keeps internal/forge/profile a leaf — internal/cli imports forge,
// not the other way around.
package profile

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/lernen-edu/lernen/internal/forge/calibration"
	"github.com/lernen-edu/lernen/internal/forge/goals"
	"github.com/lernen-edu/lernen/internal/forge/ingestion"
	"github.com/lernen-edu/lernen/internal/forge/recommendation"
)

const (
	goalsFilename          = "goals.yaml"
	startingPointFilename  = "starting_point.yaml"
	recommendationFilename = "recommendation.yaml"
	ingestionFilename      = "ingestion.yaml"
)

// stageFilenames is the ordered list of forge stage YAMLs that
// participate in --reset / --restore / --list-backups. Each entry is
// the bare filename matching what LoadGoals / LoadStartingPoint /
// LoadRecommendation expect (see GoalsPath / StartingPointPath /
// RecommendationPath).
//
// Iteration order is meaningful: it matches the pipeline order
// (Stage 0 → 1 → 2). Forward-compat: M3d ingestion appends
// "ingestion.yaml"; M3e curriculum scaffolding appends
// "curriculum.yaml". BackupAll / Restore / ListBackups iterate this
// list, so adding a new stage is a one-line change here.
var stageFilenames = []string{
	goalsFilename,
	startingPointFilename,
	recommendationFilename,
	ingestionFilename,
}

// fileMode is used when Save* creates a profile-output file. Profile
// files contain private user data — restrict to owner-read/write only
// (matches internal/config's 0o600 file convention).
const fileMode = 0o600

// dirMode is used when Save* must mkdir the profile directory.
const dirMode = 0o700

// backupTimestampLayout is the Go time.Format layout used in .bak
// filenames. No colons (Windows NTFS rejects ':' in filenames). The 'T'
// separator and 24-hour wall-clock make the timestamp human-readable
// when listing the profile dir with `ls`.
const backupTimestampLayout = "2006-01-02T15-04-05"

// displayTimestampLayout is the user-facing form used in --list-backups
// output and in console messages. ISO 8601 canonical, with colons.
const displayTimestampLayout = "2006-01-02T15:04:05"

// FormatBackupTimestamp renders ts as the filename-safe (no-colon) form
// used inside .bak filenames. Always operates in UTC so backups taken
// across timezones sort and compare correctly. Exported because the
// forge orchestrator and its tests use it for error-message recovery
// hints and test setup.
func FormatBackupTimestamp(ts time.Time) string {
	return ts.UTC().Format(backupTimestampLayout)
}

// ParseDisplayTimestamp parses a user-supplied --restore=<timestamp>
// argument. Accepts both the colon form (canonical, copied from
// --list-backups output) and the dash form (copied from a .bak
// filename via `ls`). Returns the parsed UTC time or an error
// describing both accepted layouts so the user knows what shape to
// retry. Exported because the forge orchestrator validates --restore=
// input against this function before any filesystem mutation.
func ParseDisplayTimestamp(s string) (time.Time, error) {
	if t, err := time.Parse(displayTimestampLayout, s); err == nil {
		return t.UTC(), nil
	}
	if t, err := time.Parse(backupTimestampLayout, s); err == nil {
		return t.UTC(), nil
	}
	return time.Time{}, fmt.Errorf("malformed timestamp %q; expected %s or %s", s, displayTimestampLayout, backupTimestampLayout)
}

// backupFilename composes the .bak sibling filename for stage at ts.
// stage must be one of the entries in stageFilenames; the function
// does not validate this — the caller knows.
func backupFilename(stage string, ts time.Time) string {
	return stage + "." + FormatBackupTimestamp(ts) + ".bak"
}

// parseBackupTimestamp inspects filename and, if it matches the
// .bak pattern for a known stage, returns (stage, ts, true). Returns
// (_, _, false) for: live files, .bak files with unparseable
// timestamps, .bak files for unknown stages, and any other shape.
//
// Strict by design: ListBackups walks the profile dir; anything that
// doesn't match the exact pattern is silently skipped so user-placed
// files in the dir don't appear in --list-backups output.
func parseBackupTimestamp(filename string) (string, time.Time, bool) {
	if !strings.HasSuffix(filename, ".bak") {
		return "", time.Time{}, false
	}
	trimmed := strings.TrimSuffix(filename, ".bak")
	for _, stage := range stageFilenames {
		prefix := stage + "."
		if !strings.HasPrefix(trimmed, prefix) {
			continue
		}
		tsRaw := strings.TrimPrefix(trimmed, prefix)
		if len(tsRaw) != len(backupTimestampLayout) {
			return "", time.Time{}, false
		}
		ts, err := time.Parse(backupTimestampLayout, tsRaw)
		if err != nil {
			// Defensive: a future stage filename might share this prefix; let
			// the next iteration try its parse. Today's stageFilenames have
			// non-overlapping prefixes so this is academic.
			continue
		}
		return stage, ts.UTC(), true
	}
	return "", time.Time{}, false
}

// BackupAll iterates stageFilenames; for each entry that exists as a
// live file in profileDir, atomic-renames it to its .bak sibling at
// ts. Returns the absolute paths of the .bak files created, in the
// same iteration order as stageFilenames. A non-existent profileDir
// is treated as "nothing to back up": returns empty + nil error.
//
// Atomicity: each rename is os.Rename within the same directory,
// which is POSIX-atomic. If a partway failure occurs (very rare —
// indicates filesystem error), the function returns the partial list
// with the wrapping error, so the caller can report what was saved.
// The user can recover via --restore=<ts> on the partial timestamp.
func BackupAll(profileDir string, ts time.Time) ([]string, error) {
	out := make([]string, 0, len(stageFilenames))
	for _, stage := range stageFilenames {
		src := filepath.Join(profileDir, stage)
		_, err := os.Stat(src)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return out, fmt.Errorf("profile: stat %s: %w", src, err)
		}
		dst := filepath.Join(profileDir, backupFilename(stage, ts))
		if err := os.Rename(src, dst); err != nil {
			return out, fmt.Errorf("profile: rename %s -> %s: %w", src, dst, err)
		}
		out = append(out, dst)
	}
	return out, nil
}

// BackupFromStage backs up the named stage and every downstream stage
// to .bak siblings at ts (one shared timestamp). The named stage is the
// file basename without the .yaml suffix — e.g., "starting_point" or
// "recommendation". Identifying a stage by basename matches the names
// surfaced in --list-backups output and the user-facing profile dir.
//
// Pipeline order is fixed by stageFilenames: goals < starting_point <
// recommendation. "Downstream" means every entry in stageFilenames at
// or after the named stage's index.
//
// Same-as-BackupAll semantics for the rename loop: non-existent files
// are skipped, partial-failure returns the partial list with the
// wrapping error, and a non-existent profileDir is a no-op.
//
// Returns an error if stage is not a known stage name; no filesystem
// mutation happens in that case.
func BackupFromStage(profileDir, stage string, ts time.Time) ([]string, error) {
	target := stage + ".yaml"
	startIdx := -1
	for i, fn := range stageFilenames {
		if fn == target {
			startIdx = i
			break
		}
	}
	if startIdx < 0 {
		return nil, fmt.Errorf("profile: unknown stage %q; supported: %s", stage, strings.Join(stageNames(), ", "))
	}
	out := make([]string, 0, len(stageFilenames)-startIdx)
	for _, fn := range stageFilenames[startIdx:] {
		src := filepath.Join(profileDir, fn)
		_, err := os.Stat(src)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return out, fmt.Errorf("profile: stat %s: %w", src, err)
		}
		dst := filepath.Join(profileDir, backupFilename(fn, ts))
		if err := os.Rename(src, dst); err != nil {
			return out, fmt.Errorf("profile: rename %s -> %s: %w", src, dst, err)
		}
		out = append(out, dst)
	}
	return out, nil
}

// stageNames returns the bare stage names (without the .yaml suffix)
// in canonical pipeline order. Used in error messages so the user sees
// the same naming as --list-backups output and the file basenames in
// their profile dir.
func stageNames() []string {
	out := make([]string, 0, len(stageFilenames))
	for _, fn := range stageFilenames {
		out = append(out, strings.TrimSuffix(fn, ".yaml"))
	}
	return out
}

// Restore swaps the live stage YAMLs with the .bak set at backupTs.
// Three-step:
//  1. Call BackupAll(profileDir, nowTs) to back up any current live
//     files to .bak siblings at nowTs (so the restore itself is
//     undoable via --restore=<nowTs>).
//  2. For each stage with a .bak sibling at backupTs, atomic-rename
//     that sibling to the live filename.
//  3. Stages with no .bak at backupTs are left in their post-step-1
//     state — i.e., absent from live (already moved away) — so the
//     final state matches exactly what existed at backupTs.
//
// Returns an error if no .bak files exist at backupTs (nothing to
// restore). Each individual rename inside steps 1 and 2 is atomic; a crash
// mid-batch leaves a recoverable state — the error message tells
// the user the nowTs to use for --restore recovery.
func Restore(profileDir string, backupTs, nowTs time.Time) error {
	// Verify at least one .bak exists at backupTs before any filesystem mutation.
	hasBackup := false
	for _, stage := range stageFilenames {
		if _, err := os.Stat(filepath.Join(profileDir, backupFilename(stage, backupTs))); err == nil {
			hasBackup = true
			break
		}
	}
	if !hasBackup {
		return fmt.Errorf("profile: no backup found at %s in %s", FormatBackupTimestamp(backupTs), profileDir)
	}

	// Same-second collision: BackupAll's destination would overwrite the
	// requested backup, then Step 2 would promote the overwritten state.
	// Silent data loss; fail fast before any filesystem mutation.
	if FormatBackupTimestamp(backupTs) == FormatBackupTimestamp(nowTs) {
		return fmt.Errorf("profile: Restore: backupTs and nowTs format to the same .bak filename %q; retry after one second",
			FormatBackupTimestamp(nowTs))
	}

	// Step 1: back up current live files to nowTs by reusing BackupAll.
	// The recovery hint is wrapped here, not in BackupAll, so the user
	// sees the --restore=<nowTs> path even if BackupAll returns mid-loop.
	if _, err := BackupAll(profileDir, nowTs); err != nil {
		return fmt.Errorf("profile: Restore step 1 (recover via --restore=%s): %w",
			FormatBackupTimestamp(nowTs), err)
	}

	// Step 2: promote backup set at backupTs to live.
	for _, stage := range stageFilenames {
		bak := filepath.Join(profileDir, backupFilename(stage, backupTs))
		if _, err := os.Stat(bak); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return fmt.Errorf("profile: stat %s: %w", bak, err)
		}
		live := filepath.Join(profileDir, stage)
		if err := os.Rename(bak, live); err != nil {
			return fmt.Errorf("profile: rename %s -> %s (step 2; recover via --restore=%s): %w",
				bak, live, FormatBackupTimestamp(nowTs), err)
		}
	}
	return nil
}

// BackupSet describes one timestamped backup, possibly covering only
// a subset of the three stage YAMLs. Stages elements are display names
// (no .yaml suffix) — e.g., "goals" not "goals.yaml" — sorted in
// pipeline order (goals < starting_point < recommendation) regardless
// of the directory walk order. The display naming matches spec §4's
// --list-backups output example and §5's struct documentation.
type BackupSet struct {
	Timestamp time.Time
	Stages    []string
}

// ListBackups walks profileDir, groups all parseable .bak files by
// timestamp, and returns the resulting slice sorted newest-first.
// A non-existent profileDir returns empty + nil error (consistent
// with BackupAll's "nothing to do" semantics).
//
// Stage order within each set is canonical (matches stageFilenames
// iteration order: goals < starting_point < recommendation), not
// directory-walk order. This makes --list-backups output deterministic.
// Each Stages element is the display name without the .yaml suffix.
func ListBackups(profileDir string) ([]BackupSet, error) {
	entries, err := os.ReadDir(profileDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("profile: read dir %s: %w", profileDir, err)
	}
	groups := make(map[time.Time]map[string]bool)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		stage, ts, ok := parseBackupTimestamp(entry.Name())
		if !ok {
			continue
		}
		key := ts.UTC()
		if groups[key] == nil {
			groups[key] = make(map[string]bool)
		}
		groups[key][stage] = true
	}
	out := make([]BackupSet, 0, len(groups))
	for ts, stages := range groups {
		ordered := make([]string, 0, len(stages))
		// stageFilenames order is canonical; iterate it so the
		// result is deterministic regardless of map iteration.
		// Strip the .yaml suffix so Stages contains display names.
		for _, stage := range stageFilenames {
			if stages[stage] {
				ordered = append(ordered, strings.TrimSuffix(stage, ".yaml"))
			}
		}
		out = append(out, BackupSet{Timestamp: ts, Stages: ordered})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Timestamp.After(out[j].Timestamp)
	})
	return out, nil
}

// GoalsPath returns the absolute path to goals.yaml inside profileDir.
func GoalsPath(profileDir string) string {
	return filepath.Join(profileDir, goalsFilename)
}

// StartingPointPath returns the absolute path to starting_point.yaml
// inside profileDir.
func StartingPointPath(profileDir string) string {
	return filepath.Join(profileDir, startingPointFilename)
}

// RecommendationPath returns the absolute path to recommendation.yaml
// inside profileDir.
func RecommendationPath(profileDir string) string {
	return filepath.Join(profileDir, recommendationFilename)
}

// LoadGoals reads goals.yaml from profileDir and returns the parsed
// struct. Returns (nil, nil) if the file does not exist — the resume
// detector uses this as the "Stage 0 not yet complete" signal.
// Returns (nil, err) on any other failure (read error, malformed YAML).
//
// LoadGoals does not call Validate(); callers do that when they need
// the contract enforced.
func LoadGoals(profileDir string) (*goals.Goals, error) {
	var g goals.Goals
	found, err := loadYAML(GoalsPath(profileDir), &g)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, nil
	}
	return &g, nil
}

// LoadStartingPoint reads starting_point.yaml from profileDir. Same
// (nil, nil) on absent / (nil, err) on other failure shape as
// LoadGoals; the resume detector treats absent as "Stage 1 not yet
// complete". Like LoadGoals, does not call Validate() on the result.
func LoadStartingPoint(profileDir string) (*calibration.StartingPoint, error) {
	var sp calibration.StartingPoint
	found, err := loadYAML(StartingPointPath(profileDir), &sp)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, nil
	}
	return &sp, nil
}

// LoadRecommendation reads recommendation.yaml from profileDir. Same
// (nil, nil)-on-absent / (nil, err)-on-other-failure shape as
// LoadGoals; the resume detector treats absent as "Stage 2 not yet
// complete". Like LoadGoals, does not call Validate() on the result.
func LoadRecommendation(profileDir string) (*recommendation.Recommendation, error) {
	var rec recommendation.Recommendation
	found, err := loadYAML(RecommendationPath(profileDir), &rec)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, nil
	}
	return &rec, nil
}

// SaveGoals atomically writes g to goals.yaml in profileDir. The
// directory is created if missing. See atomicWriteYAML for the
// crash-safety invariant.
func SaveGoals(profileDir string, g *goals.Goals) error {
	return atomicWriteYAML(profileDir, GoalsPath(profileDir), g)
}

// SaveStartingPoint atomically writes sp to starting_point.yaml in
// profileDir. The directory is created if missing.
func SaveStartingPoint(profileDir string, sp *calibration.StartingPoint) error {
	return atomicWriteYAML(profileDir, StartingPointPath(profileDir), sp)
}

// SaveRecommendation atomically writes rec to recommendation.yaml in
// profileDir. The directory is created if missing.
func SaveRecommendation(profileDir string, rec *recommendation.Recommendation) error {
	return atomicWriteYAML(profileDir, RecommendationPath(profileDir), rec)
}

// IngestionPath returns the absolute path where SaveIngestion will
// write — joins profileDir with the ingestion filename. Pure path
// computation; does not stat or create.
func IngestionPath(profileDir string) string {
	return filepath.Join(profileDir, ingestionFilename)
}

// LoadIngestion reads ingestion.yaml from profileDir. Returns nil
// (with nil error) when the file does not exist — the resume detector
// uses this signal as "Stage 3 not yet run." Any other read or
// unmarshal error returns the error.
func LoadIngestion(profileDir string) (*ingestion.Ingestion, error) {
	var ing ingestion.Ingestion
	found, err := loadYAML(IngestionPath(profileDir), &ing)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, nil
	}
	return &ing, nil
}

// SaveIngestion writes ing to ingestion.yaml under profileDir using
// the atomic-rename helper. The profile directory is created with
// dirMode (0o700) if missing.
func SaveIngestion(profileDir string, ing *ingestion.Ingestion) error {
	if ing == nil {
		return fmt.Errorf("profile: SaveIngestion: ingestion is nil")
	}
	return atomicWriteYAML(profileDir, IngestionPath(profileDir), ing)
}

// loadYAML reads path and unmarshals into dst. Returns (true, nil) on
// success, (false, nil) when the file does not exist, (false, err) on
// any other failure (read error, malformed YAML). Caller decides
// whether absent is success or error.
func loadYAML(path string, dst any) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("profile: read %s: %w", path, err)
	}
	if err := yaml.Unmarshal(data, dst); err != nil {
		return false, fmt.Errorf("profile: parse %s: %w", path, err)
	}
	return true, nil
}

// atomicWriteYAML marshals v as YAML and atomically writes to dest
// inside dir. Creates dir if missing.
//
// Implementation: write to a tempfile in dir, then rename over dest.
// Rename within the same filesystem is atomic, so a crash mid-write
// leaves either the old file (or no file) at dest, never a half-file.
// The tempfile is cleaned up on every error path so a failure does
// not leave debris in the profile dir.
func atomicWriteYAML(dir, dest string, v any) error {
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return fmt.Errorf("profile: mkdir %s: %w", dir, err)
	}
	data, err := yaml.Marshal(v)
	if err != nil {
		return fmt.Errorf("profile: marshal %s: %w", filepath.Base(dest), err)
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(dest)+".*.tmp")
	if err != nil {
		return fmt.Errorf("profile: create tempfile: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("profile: write tempfile: %w", err)
	}
	if err := tmp.Chmod(fileMode); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("profile: chmod tempfile: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("profile: sync tempfile: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("profile: close tempfile: %w", err)
	}
	if err := os.Rename(tmpPath, dest); err != nil {
		cleanup()
		return fmt.Errorf("profile: rename %s -> %s: %w", tmpPath, dest, err)
	}
	return nil
}
