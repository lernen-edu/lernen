package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lernen-edu/lernen/internal/backends"
	"github.com/lernen-edu/lernen/internal/competency"
	"github.com/lernen-edu/lernen/internal/config"
	"github.com/lernen-edu/lernen/internal/curriculum"
	"github.com/lernen-edu/lernen/internal/languages"
	gatepkg "github.com/lernen-edu/lernen/internal/phase1/gate"
	"github.com/spf13/cobra"
)

type GateRunConfig struct {
	CurriculumID string
	ManifestDir  string
	ProgressRoot string
	DataRoot     string
	Ctx          context.Context
	// Lines is the shared session stdin channel, owned by runGate (the
	// single reader spans the resume/precondition prompts AND this exam
	// loop, so a prologue prompt never strands subsequent exam input).
	Lines        <-chan string
	Out          io.Writer
	Plan         gatepkg.AttemptPlan
	Fixtures     languages.GateFixtures
	Adapter      languages.LanguageAdapter
	Precondition gatepkg.PreconditionSnapshot
	// Backend is a LAZY factory — called at most once, only when the
	// comprehension component is reached (the single sanctioned backend
	// touchpoint in v0). The build/debug paths never call it (AI-off).
	Backend func() (backends.Backend, error)
}

type GateDeps struct {
	SessionRunner func(GateRunConfig) error
	// SkipPreFlight disables the pytest/pytest-json-report pre-flight check.
	// Set to true in tests that need to exercise logic past the pre-flight
	// without requiring a real Python toolchain in the test environment.
	// Production always leaves this false.
	SkipPreFlight bool
}

func ProductionGateDeps() GateDeps {
	return GateDeps{SessionRunner: productionGateRunner}
}

func NewGateCmd(deps GateDeps) *cobra.Command {
	var manifestDir string
	cmd := &cobra.Command{
		Use:   "gate <curriculum-id>",
		Short: "Attempt the Phase 1→2 capability gate",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runGate(cmd.Context(), args[0], manifestDir, cmd.OutOrStdout(), cmd.InOrStdin(), deps)
		},
	}
	cmd.Flags().StringVar(&manifestDir, "manifest-dir", "", "override the manifests directory (default: XDG data dir)")
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	return cmd
}

func runGate(ctx context.Context, curriculumID, manifestDirArg string, out io.Writer, in io.Reader, deps GateDeps) error {
	manifestDir, err := resolveManifestDir(manifestDirArg)
	if err != nil {
		return err
	}
	curr, err := curriculum.Load(filepath.Join(manifestDir, curriculumID))
	if err != nil {
		return fmt.Errorf("gate: load curriculum: %w", err)
	}
	progressRoot, state, err := loadProgressFor(curriculumID)
	if err != nil {
		return err
	}
	adapter, ok := languages.Get(strings.ToLower(curr.Metadata.Language))
	if !ok {
		return fmt.Errorf("gate: no language adapter for %q", curr.Metadata.Language)
	}
	fixtures, err := adapter.GateFixtures()
	if err != nil {
		return fmt.Errorf("gate: load fixtures: %w", err)
	}
	if !deps.SkipPreFlight {
		tc := adapter.ToolchainCheck(ctx)
		if !toolAvailable(tc, "pytest") || !toolAvailable(tc, "pytest-json-report") {
			fmt.Fprintln(os.Stderr, "gate needs pytest + pytest-json-report: pip install pytest pytest-json-report")
			return fmt.Errorf("gate: pre-flight failed: pytest or pytest-json-report not available")
		}
	}
	statuses := competency.Aggregate(state, curr)
	met := competency.GateReady(statuses)
	fmt.Fprintln(out, competency.Render(state, curr))
	foundMet, foundTotal := countFoundation(statuses)
	snap := gatepkg.PreconditionSnapshot{Met: met, FoundationMet: foundMet, FoundationTotal: foundTotal}

	resumable, err := gatepkg.HasInProgress(progressRoot, curriculumID)
	if err != nil {
		return err
	}
	// The single session stdin reader: created here, AFTER the
	// missing-curriculum / HasInProgress error paths (those must still
	// return before any reader/prompt) and BEFORE the prompts, so the
	// SAME reader serves the prompts AND the exam loop. defer spans the
	// whole command including deps.SessionRunner.
	lines, cancelReader := newLineReader(ctx, in)
	defer cancelReader()

	resumeAccepted := false
	if resumable {
		resumeAccepted = promptCh(ctx, lines, out, "An in-progress gate attempt exists. Resume it? [y/N]: ")
	}
	plan, err := gatepkg.PlanAttempt(progressRoot, curriculumID, resumeAccepted)
	if err != nil {
		return err
	}
	if !met {
		fmt.Fprintln(out, "Foundation-competency precondition is NOT met (see table above).")
		if !promptCh(ctx, lines, out, "Attempt the exam anyway? It will not pass overall until the precondition is met. [y/N]: ") {
			return nil
		}
	}

	dataRoot, err := DataDir()
	if err != nil {
		return fmt.Errorf("gate: resolve data dir: %w", err)
	}
	return deps.SessionRunner(GateRunConfig{
		CurriculumID: curriculumID, ManifestDir: manifestDir, ProgressRoot: progressRoot,
		DataRoot: dataRoot, Ctx: ctx, Lines: lines, Out: out,
		Plan: plan, Fixtures: fixtures, Adapter: adapter, Precondition: snap,
		Backend: productionGateBackend, // lazy; defined below
	})
}

func countFoundation(statuses []competency.CompetencyStatus) (met, total int) {
	for _, s := range statuses {
		if s.InManifest && s.Tier == curriculum.TierFoundation {
			total++
			if s.Met {
				met++
			}
		}
	}
	return
}

// newLineReader spawns the single stdin reader for the whole gate
// command. Exactly one goroutine ever reads `r`; trimmed lines are
// sent on the returned channel (never closed — consumers handle
// "no more input" via ctx). cancel() stops the goroutine; the caller
// MUST defer it.
func newLineReader(ctx context.Context, r io.Reader) (<-chan string, context.CancelFunc) {
	rctx, cancel := context.WithCancel(ctx)
	ch := make(chan string)
	go func() {
		sc := bufio.NewScanner(r)
		for sc.Scan() {
			select {
			case ch <- strings.TrimSpace(sc.Text()):
			case <-rctx.Done():
				return
			}
		}
	}()
	return ch, cancel
}

// promptCh asks a y/N question, reading exactly ONE line off the
// shared session channel (so it never strands subsequent input).
// ctx cancellation or input exhaustion ⇒ treated as "no".
func promptCh(ctx context.Context, lines <-chan string, out io.Writer, q string) bool {
	fmt.Fprint(out, q)
	select {
	case line, ok := <-lines:
		if !ok {
			return false
		}
		a := strings.ToLower(strings.TrimSpace(line))
		return a == "y" || a == "yes"
	case <-ctx.Done():
		return false
	}
}

// productionGateBackend constructs the real inference backend exactly as
// `lernen work` does: load config from the standard path, then build the
// backend via the shared productionBackend factory. Called LAZILY by the
// exam loop only at the comprehension component (the single sanctioned
// backend touchpoint — the build/debug paths are AI-off). No HealthCheck
// here: a broken backend surfaces as OutcomeInfraError from the
// comprehension grader (non-terminal/resumable), never a manufactured FAIL.
func productionGateBackend() (backends.Backend, error) {
	cfgPath, err := ConfigFile()
	if err != nil {
		return nil, fmt.Errorf("gate: resolve config path: %w", err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return nil, fmt.Errorf("gate: load config: %w", err)
	}
	be, err := productionBackend(&cfg)
	if err != nil {
		return nil, fmt.Errorf("gate: construct backend: %w", err)
	}
	return be, nil
}

// productionGateRunner is the imperative exam loop — the SessionRunner.
// It owns NO stdin reader and spawns NO goroutine: runGate created the
// single session reader BEFORE the prologue prompts and threads it in as
// cfg.Lines (its defer cancelReader() spans this call). Components run
// strictly sequentially so exactly one consumer drains cfg.Lines at a
// time (no cross-component theft). AI-off-build invariant: cfg.Backend()
// is invoked exactly once, only in the comprehension branch. infra_error
// is non-terminal: paused + retained sidecar + no AppendAttempt + return
// nil. A FAIL verdict is a normal completed attempt (return nil); only
// IO/infra returns a Go error.
func productionGateRunner(cfg GateRunConfig) error {
	// Fixture set: on resume re-use the persisted set (SelectSet is
	// deterministic for the same attempt#, but the persisted set is the
	// durable contract) and seed components from the sidecar.
	fs := gatepkg.SelectSet(cfg.Fixtures, cfg.CurriculumID, cfg.Plan.AttemptNumber)
	components := map[gatepkg.Component]gatepkg.ComponentOutcome{}
	startedAt := time.Now().UTC()
	if cfg.Plan.Resumed && cfg.Plan.Sidecar != nil {
		fs = cfg.Plan.Sidecar.FixtureSet
		startedAt = cfg.Plan.Sidecar.StartedAt
		for c, o := range cfg.Plan.Sidecar.Components {
			components[c] = o
		}
	}

	snapshot := func() map[gatepkg.Component]gatepkg.ComponentOutcome {
		cp := make(map[gatepkg.Component]gatepkg.ComponentOutcome, len(components))
		for c, o := range components {
			cp[c] = o
		}
		return cp
	}
	saveSidecar := func() error {
		return gatepkg.SaveSidecar(cfg.ProgressRoot, &gatepkg.Sidecar{
			SchemaVersion: gatepkg.CurrentSchemaVersion,
			CurriculumID:  cfg.CurriculumID,
			AttemptNumber: cfg.Plan.AttemptNumber,
			StartedAt:     startedAt,
			FixtureSet:    fs,
			Precondition:  cfg.Precondition,
			Components:    snapshot(),
		})
	}
	pause := func() error {
		fmt.Fprintln(cfg.Out, "Gate paused (environment issue). Fix it and re-run `lernen gate` to resume.")
		return saveSidecar()
	}

	for _, c := range gatepkg.ComponentOrder() {
		if o, ok := components[c]; ok && o.Terminal() {
			continue // resume: this component already finalized
		}

		var outcome gatepkg.ComponentOutcome
		switch c {
		case gatepkg.ComponentBuild:
			bf, ok := findBuild(cfg.Fixtures.Build, fs.Build)
			if !ok {
				return fmt.Errorf("gate: build fixture %q not found", fs.Build)
			}
			o, _, err := gatepkg.RunBuildInteractive(cfg.Ctx, cfg.DataRoot, cfg.CurriculumID, bf, cfg.Adapter.TestRunner(), cfg.Lines, cfg.Out)
			if err != nil {
				o = gatepkg.OutcomeInfraError
			}
			outcome = o

		case gatepkg.ComponentComprehension:
			be, berr := cfg.Backend()
			if berr != nil {
				fmt.Fprintf(cfg.Out, "Comprehension backend unavailable: %v\n", berr)
				outcome = gatepkg.OutcomeInfraError
				break
			}
			outcome = runComprehension(cfg, be, fs.Comprehension, cfg.Lines)

		case gatepkg.ComponentDebug:
			outcome = gatepkg.OutcomePass
			for _, id := range fs.Debug {
				df, ok := findDebug(cfg.Fixtures.Debug, id)
				if !ok {
					return fmt.Errorf("gate: debug fixture %q not found", id)
				}
				o, _, err := gatepkg.RunDebugInteractive(cfg.Ctx, cfg.DataRoot, cfg.CurriculumID, df, cfg.Adapter.TestRunner(), cfg.Lines, cfg.Out)
				if err != nil {
					o = gatepkg.OutcomeInfraError
				}
				outcome = aggregateOutcome(outcome, o)
			}
		}

		components[c] = outcome
		if !outcome.Terminal() {
			// infra_error: pause, retain the sidecar (resumable), do NOT
			// AppendAttempt, return nil. Never a manufactured FAIL.
			if err := pause(); err != nil {
				return err
			}
			return nil
		}
		// Terminal: checkpoint and continue.
		if err := saveSidecar(); err != nil {
			return err
		}
	}

	// Finalize — reached only when all 3 components are terminal.
	allTerminal, overallPass := gatepkg.Verdict(components, cfg.Precondition.Met)
	if !allTerminal {
		// Defensive: the loop guarantees this, but never finalize a
		// non-terminal attempt.
		if err := pause(); err != nil {
			return err
		}
		return nil
	}
	att := gatepkg.Attempt{
		AttemptNumber: cfg.Plan.AttemptNumber,
		StartedAt:     startedAt,
		CompletedAt:   time.Now().UTC(),
		FixtureSet:    fs,
		Precondition:  cfg.Precondition,
		Components:    snapshot(),
		OverallPass:   overallPass,
	}
	if err := gatepkg.AppendAttempt(cfg.ProgressRoot, cfg.CurriculumID, att); err != nil {
		return err
	}
	if err := gatepkg.ClearSidecar(cfg.ProgressRoot, cfg.CurriculumID); err != nil {
		return err
	}

	verdict := "FAIL"
	if overallPass {
		verdict = "PASS"
	}
	fmt.Fprintf(cfg.Out, "Gate verdict: %s\n", verdict)
	fmt.Fprintf(cfg.Out, "Precondition: met=%v %d/%d foundation\n",
		cfg.Precondition.Met, cfg.Precondition.FoundationMet, cfg.Precondition.FoundationTotal)
	for _, comp := range gatepkg.ComponentOrder() {
		fmt.Fprintf(cfg.Out, "  %s: %s\n", comp, components[comp])
	}
	if !overallPass {
		fmt.Fprintf(cfg.Out, "The gate is re-attemptable — run `lernen gate %s` again when ready.\n", cfg.CurriculumID)
	}
	return nil
}

// runComprehension grades the 3 selected comprehension samples. The
// backend is passed in (already constructed once by the caller). Returns
// the aggregate outcome: any infra ⇒ infra; else any fail ⇒ fail; else
// pass.
func runComprehension(cfg GateRunConfig, be backends.Backend, ids []string, lines <-chan string) gatepkg.ComponentOutcome {
	agg := gatepkg.OutcomePass
	for _, id := range ids {
		cf, ok := findComprehension(cfg.Fixtures.Comprehension, id)
		if !ok {
			fmt.Fprintf(cfg.Out, "comprehension fixture %q not found\n", id)
			return gatepkg.OutcomeInfraError
		}
		fmt.Fprintf(cfg.Out, "\nRead this snippet:\n\n%s\n\n", cf.Snippet)
		fmt.Fprintln(cfg.Out, "1. What exactly does it output? (one line)")
		predicted, err := readLine(cfg.Ctx, lines, cfg.Out)
		if err != nil {
			return gatepkg.OutcomeInfraError
		}
		fmt.Fprintln(cfg.Out, "2. What is the most significant defect or risk? (one line)")
		issue, err := readLine(cfg.Ctx, lines, cfg.Out)
		if err != nil {
			return gatepkg.OutcomeInfraError
		}
		so, serr := gatepkg.RunComprehensionSample(cfg.Ctx, be, cf, predicted, issue)
		if serr != nil {
			so = gatepkg.OutcomeInfraError
		}
		agg = aggregateOutcome(agg, so)
	}
	return agg
}

// aggregateOutcome folds a sample/sub-component outcome into a running
// component aggregate: infra dominates fail dominates pass.
func aggregateOutcome(agg, next gatepkg.ComponentOutcome) gatepkg.ComponentOutcome {
	if agg == gatepkg.OutcomeInfraError || next == gatepkg.OutcomeInfraError {
		return gatepkg.OutcomeInfraError
	}
	if agg == gatepkg.OutcomeFail || next == gatepkg.OutcomeFail {
		return gatepkg.OutcomeFail
	}
	return gatepkg.OutcomePass
}

// readLine blocks for the next pre-trimmed line, the only sanctioned exam
// input read. A closed/exhausted channel parks on ctx (the caller must
// not treat EOF as an answer); ctx cancellation returns ctx.Err().
func readLine(ctx context.Context, lines <-chan string, _ io.Writer) (string, error) {
	select {
	case l, ok := <-lines:
		if !ok {
			<-ctx.Done()
			return "", ctx.Err()
		}
		return l, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func findBuild(fxs []languages.BuildFixture, id string) (languages.BuildFixture, bool) {
	for _, f := range fxs {
		if f.ID == id {
			return f, true
		}
	}
	return languages.BuildFixture{}, false
}

func findComprehension(fxs []languages.ComprehensionFixture, id string) (languages.ComprehensionFixture, bool) {
	for _, f := range fxs {
		if f.ID == id {
			return f, true
		}
	}
	return languages.ComprehensionFixture{}, false
}

func findDebug(fxs []languages.DebugFixture, id string) (languages.DebugFixture, bool) {
	for _, f := range fxs {
		if f.ID == id {
			return f, true
		}
	}
	return languages.DebugFixture{}, false
}
