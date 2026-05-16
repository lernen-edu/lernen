package cli

import (
	"context"
	"fmt"
	"io"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/lernen-edu/lernen/internal/competency"
	"github.com/lernen-edu/lernen/internal/curriculum"
)

// StatusDeps is the dependency-injection surface for `lernen status`.
// The command is read-only (no AI, no TUI, no backend), so no fields
// are needed — an empty struct suffices.
type StatusDeps struct{}

// ProductionStatusDeps returns the StatusDeps for the shipped binary.
func ProductionStatusDeps() StatusDeps { return StatusDeps{} }

// NewStatusCmd builds the `lernen status` Cobra command.
//
// Usage:
//
//	lernen status <curriculum-id> [--manifest-dir <path>]
//
// Prints the competency table followed by the chapter progress table.
// Read-only: no AI, no TUI, no progress.Save.
func NewStatusCmd(_ StatusDeps) *cobra.Command {
	var manifestDir string
	cmd := &cobra.Command{
		Use:           "status <curriculum-id>",
		Short:         "Show competency progress and gate-readiness (read-only, no AI).",
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runStatus(cmd.Context(), args[0], manifestDir, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&manifestDir, "manifest-dir", "",
		"Override manifests directory (default: XDG-resolved $DataDir/manifests)")
	return cmd
}

// runStatus is the read-only implementation of `lernen status`:
//  1. Resolve manifest dir and load curriculum.
//  2. Load progress (nil == no progress recorded yet). competency.Render is
//     nil-safe (Aggregate guards nil); renderProgressTable is NOT — it
//     dereferences state, so the nil case is guarded explicitly below.
//     Do not remove that guard.
//  3. Write competency.Render output, blank line, renderProgressTable output.
func runStatus(_ context.Context, curriculumID, manifestDirArg string, out io.Writer) error {
	manifestDir, err := resolveManifestDir(manifestDirArg)
	if err != nil {
		return err
	}
	curr, err := curriculum.Load(filepath.Join(manifestDir, curriculumID))
	if err != nil {
		return err
	}
	// loadProgressFor is the shared 3-return helper from progress_helpers.go
	// (Task 10). Status is read-only; progressRoot is intentionally discarded.
	_, state, err := loadProgressFor(curriculumID)
	if err != nil {
		return err
	}

	// competency.Render already ends with \n; use Fprint (not Fprintln) to
	// avoid a spurious extra newline before the blank-line separator.
	fmt.Fprint(out, competency.Render(state, curr))
	fmt.Fprintln(out)
	// renderProgressTable dereferences state — guard nil here so status
	// cannot panic when there is no recorded progress yet. Do not remove.
	if state != nil {
		fmt.Fprintln(out, renderProgressTable(state, curr))
	} else {
		fmt.Fprintln(out, "Progress for "+curr.Metadata.Name+":\n\n  (no progress recorded yet)")
	}
	return nil
}
