package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/lernen-edu/lernen/internal/backends"
	"github.com/lernen-edu/lernen/internal/config"
	"github.com/lernen-edu/lernen/internal/docs"
)

// SetupDeps is the dependency-injection surface for `lernen setup`.
type SetupDeps struct {
	In  io.Reader // production: os.Stdin; tests: strings.NewReader
	Out io.Writer // production: os.Stdout; tests: *bytes.Buffer
	Err io.Writer // production: os.Stderr; tests: *bytes.Buffer

	// BackendFactory and DocsProviderFactory mirror the shapes used by
	// `lernen work` and `lernen docs`. Setup constructs each chosen
	// component once to run a HealthCheck, then writes the config.
	BackendFactory      func(*config.Config) (backends.Backend, error)
	DocsProviderFactory func(*config.Config) (docs.Provider, error)

	// ConfigPath optionally overrides the default ConfigFile() lookup.
	// Tests usually point this at a path inside t.TempDir() so production
	// config is untouched.
	ConfigPath string
}

// ProductionSetupDeps returns the SetupDeps wired for the shipped binary.
func ProductionSetupDeps() SetupDeps {
	return SetupDeps{
		In:                  os.Stdin,
		Out:                 os.Stdout,
		Err:                 os.Stderr,
		BackendFactory:      productionBackend,
		DocsProviderFactory: productionDocsProvider,
	}
}

// NewSetupCmd builds the `lernen setup` Cobra command.
func NewSetupCmd(deps SetupDeps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Configure the backend and docs provider, then write config.toml.",
		Long: `Walk through backend selection, docs provider selection, and
HealthCheck verification. Writes the resulting config to the conventional
path (~/.config/lernen/config.toml on Linux/Mac, %AppData%\lernen\config.toml
on Windows) atomically, with permissions 0600.

HealthCheck failures are warnings, not aborts: if you run setup before
exporting the API-key env var, setup writes the config anyway and tells
you what to fix before running ` + "`lernen work`" + `.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSetup(cmd.Context(), deps)
		},
	}
	// main() prints the returned error; suppress cobra's duplicate.
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	return cmd
}

func runSetup(ctx context.Context, deps SetupDeps) error {
	if deps.In == nil || deps.Out == nil || deps.Err == nil {
		return errors.New("setup: In/Out/Err are required (programmer error)")
	}
	if deps.BackendFactory == nil || deps.DocsProviderFactory == nil {
		return errors.New("setup: factories are required (programmer error)")
	}

	cfgPath := deps.ConfigPath
	if cfgPath == "" {
		p, err := ConfigFile()
		if err != nil {
			return fmt.Errorf("setup: resolve config path: %w", err)
		}
		cfgPath = p
	}

	cfg, _ := config.Load(cfgPath) // an existing-but-broken file becomes the starting point
	fileExists := configFileExists(cfgPath)

	fmt.Fprintf(deps.Out, "lernen setup — writing %s\n\n", cfgPath)

	// Orienting note. Without this, users have repeatedly typed key
	// VALUES at env-var-name prompts; the values land on disk and have
	// to be rotated. Surface the pattern first so the prompts read
	// unambiguously.
	fmt.Fprintln(deps.Out, "Lernen never stores API key values on disk. This wizard captures the NAME of")
	fmt.Fprintln(deps.Out, "each backend's environment variable (e.g. OPENROUTER_API_KEY); set the actual")
	fmt.Fprintln(deps.Out, "key in your shell with `export OPENROUTER_API_KEY=...` after setup completes.")
	fmt.Fprintln(deps.Out)

	p := newPrompter(deps.In, deps.Out)

	if fileExists {
		if !p.askYesNo("Existing config found. Overwrite?", false) {
			fmt.Fprintln(deps.Out, "Aborted; existing config left unchanged.")
			return nil
		}
		fmt.Fprintln(deps.Out)
	}

	// --- backend section ---
	fmt.Fprintln(deps.Out, "Available backends:")
	fmt.Fprintln(deps.Out, "  1. openrouter — multi-provider proxy via OpenRouter (API key)")
	fmt.Fprintln(deps.Out, "  2. openai     — OpenAI direct, no CLI install (API key)")
	fmt.Fprintln(deps.Out, "  3. google     — Google Gemini direct, no CLI install (API key)")
	fmt.Fprintln(deps.Out, "  4. codex      — Codex CLI subprocess (supports ChatGPT Plus subscription)")
	fmt.Fprintln(deps.Out, "  5. gemini     — gemini-cli subprocess (supports Google account auth)")
	fmt.Fprintln(deps.Out)

	backendOptions := []string{"openrouter", "openai", "google", "codex", "gemini"}
	defIdx := indexOf(backendOptions, cfg.Backend)
	if defIdx < 0 {
		defIdx = 0
	}
	chosen := p.askChoice("Backend", backendOptions, defIdx)
	cfg.Backend = backendOptions[chosen]

	switch cfg.Backend {
	case "openrouter":
		printModelClassHint(deps.Out)
		cfg.OpenRouter.Model = p.ask("OpenRouter model", cfg.OpenRouter.Model)
		cfg.OpenRouter.APIKeyEnv = p.askEnvVarName("OpenRouter env var name (NOT the key value)", cfg.OpenRouter.APIKeyEnv)
	case "openai":
		printModelClassHint(deps.Out)
		cfg.OpenAI.Model = p.ask("OpenAI model", cfg.OpenAI.Model)
		cfg.OpenAI.APIKeyEnv = p.askEnvVarName("OpenAI env var name (NOT the key value)", cfg.OpenAI.APIKeyEnv)
	case "google":
		printModelClassHint(deps.Out)
		cfg.Google.Model = p.ask("Google model", cfg.Google.Model)
		cfg.Google.APIKeyEnv = p.askEnvVarName("Google env var name (NOT the key value)", cfg.Google.APIKeyEnv)
	case "codex":
		fmt.Fprintln(deps.Out, "(env var optional if you've authenticated via `codex login`)")
		fmt.Fprintln(deps.Out, "WARNING: the codex CLI auto-discovers skills (e.g. ~/.codex/superpowers/")
		fmt.Fprintln(deps.Out, "skills/) and AGENTS.md files, and runs read-only tool-use during every turn.")
		fmt.Fprintln(deps.Out, "This leaks codex's coding-agent posture into Lernen's tutor session. Disable")
		fmt.Fprintln(deps.Out, "or move those before running `lernen work`. See docs/BUILD_ORDER.md M2.5 for")
		fmt.Fprintln(deps.Out, "the workaround details.")
		cfg.Codex.Model = p.ask("Codex model", cfg.Codex.Model)
		cfg.Codex.APIKeyEnv = p.askEnvVarName("Codex env var name (NOT the key value)", cfg.Codex.APIKeyEnv)
	case "gemini":
		fmt.Fprintln(deps.Out, "(env var optional if you've authenticated via `gemini auth login`)")
		fmt.Fprintln(deps.Out, "WARNING: gemini-cli auto-discovers skills (~/.gemini/skills/, ~/.agents/skills/)")
		fmt.Fprintln(deps.Out, "and GEMINI.md/AGENTS.md files. Their names are always injected into the system")
		fmt.Fprintln(deps.Out, "prompt and may cause tool-use during tutor turns. Disable or move those before")
		fmt.Fprintln(deps.Out, "running `lernen work`. See docs/BUILD_ORDER.md M2.5 for the workaround.")
		cfg.Gemini.Model = p.ask("Gemini model", cfg.Gemini.Model)
		cfg.Gemini.APIKeyEnv = p.askEnvVarName("Gemini env var name (NOT the key value)", cfg.Gemini.APIKeyEnv)
	}

	runHealthCheck(ctx, deps.Out, deps.Err, "backend", backendEnvName(cfg), backendRequiresEnvVar(cfg.Backend), func() error {
		b, err := deps.BackendFactory(&cfg)
		if err != nil {
			return err
		}
		return b.HealthCheck(ctx)
	})

	fmt.Fprintln(deps.Out)

	// --- docs section ---
	docsOptions := []string{"null", "context7"}
	defIdx = indexOf(docsOptions, cfg.DocsProvider)
	if defIdx < 0 {
		defIdx = 0
	}
	chosen = p.askChoice("Docs provider", docsOptions, defIdx)
	cfg.DocsProvider = docsOptions[chosen]

	if cfg.DocsProvider == "context7" {
		cfg.Context7.APIKeyEnv = p.askEnvVarName("Context7 env var name (NOT the key value)", cfg.Context7.APIKeyEnv)
		runHealthCheck(ctx, deps.Out, deps.Err, "docs", cfg.Context7.APIKeyEnv, true /* env var required */, func() error {
			provider, err := deps.DocsProviderFactory(&cfg)
			if err != nil {
				return err
			}
			if closer, ok := provider.(io.Closer); ok {
				defer closer.Close()
			}
			return provider.HealthCheck(ctx)
		})
	}

	// --- save ---
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("setup: %w", err)
	}
	if err := config.Save(cfg, cfgPath); err != nil {
		return fmt.Errorf("setup: %w", err)
	}

	fmt.Fprintf(deps.Out, "\nWrote %s.\n\n", cfgPath)
	printExportReminder(deps.Out, cfg)
	fmt.Fprintln(deps.Out, "Then: lernen work <curriculum>")
	return nil
}

// printExportReminder lists the env vars the user still needs to set in
// their shell before running `lernen work`. The values are intentionally
// left as placeholders; setup never sees the actual key values. For
// CLI-subprocess backends (codex, gemini) the message is softened —
// printModelClassHint surfaces the dogfood-confirmed minimum model
// guideline before the user picks their model. Small instruction-tuned
// open-weight models (Qwen 32B, Gemma 31B) tend to bullet-summarize
// the system prompt as scratchpad output instead of replying directly,
// and coder-tuned variants are adversarial against the no-code Phase 1
// firewall. Frontier or 70B+ general-instruction models behave well.
func printModelClassHint(out io.Writer) {
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Model class: pick a frontier model (gpt-5.4, gemini-2.5-flash,")
	fmt.Fprintln(out, "claude-3.7-sonnet) or a 70B+ general-instruction model. Smaller")
	fmt.Fprintln(out, "open-weight models (Qwen 32B, Gemma 31B, Mistral 22B) tend to leak")
	fmt.Fprintln(out, "the system prompt as scratchpad output, and coder-tuned variants")
	fmt.Fprintln(out, "fight the no-code Phase 1 firewall. See docs/BUILD_ORDER.md M2.5.")
	fmt.Fprintln(out)
}

// the env var is one of two valid auth paths, and the CLI's own login
// is the alternative.
func printExportReminder(out io.Writer, cfg config.Config) {
	backendEnv := backendEnvName(cfg)
	cliBackend := !backendRequiresEnvVar(cfg.Backend)
	docsEnv := ""
	if cfg.DocsProvider == "context7" {
		docsEnv = cfg.Context7.APIKeyEnv
	}

	if backendEnv == "" && docsEnv == "" {
		return
	}

	if backendEnv != "" && cliBackend {
		fmt.Fprintf(out, "If you haven't authenticated via the %s CLI's own login flow,\n", cfg.Backend)
		fmt.Fprintf(out, "set the env var instead: export %s='your-key-here'\n\n", backendEnv)
	} else if backendEnv != "" {
		fmt.Fprintln(out, "Before running `lernen work`, set this in your shell:")
		fmt.Fprintf(out, "  export %s='your-key-here'\n\n", backendEnv)
	}

	if docsEnv != "" {
		fmt.Fprintln(out, "Set the docs API-key env var:")
		fmt.Fprintf(out, "  export %s='your-key-here'\n\n", docsEnv)
	}
}

// runHealthCheck attempts a HealthCheck and prints the outcome. If the
// env var is empty AND requiresEnvVar is true (HTTP backends, Context7),
// the check is skipped with a warning since the env var is the only auth
// path. If requiresEnvVar is false (codex/gemini CLI subprocess), the
// check is attempted regardless — the CLI may have its own auth state
// that succeeds without the env var.
//
// A failed check is a warning, not a fatal error — the setup wizard's
// job is to write the config, not to gate on a working env.
func runHealthCheck(ctx context.Context, out, errOut io.Writer, label, envName string, requiresEnvVar bool, check func() error) {
	_ = ctx
	if envName == "" {
		return
	}
	if requiresEnvVar && os.Getenv(envName) == "" {
		fmt.Fprintf(errOut, "warning: %s is not currently set; skipping %s health check (set it before running `lernen work`)\n", envName, label)
		return
	}
	if err := check(); err != nil {
		fmt.Fprintf(errOut, "warning: %s health check failed: %v\n", label, err)
		return
	}
	fmt.Fprintf(out, "%s reachable.\n", strings.ToUpper(label[:1])+label[1:])
}

// backendRequiresEnvVar reports whether the given backend treats the
// API-key env var as its only auth path. HTTP-direct backends do; the
// CLI-subprocess backends accept either the env var OR the CLI's own
// auth state and so do not require the env var.
func backendRequiresEnvVar(backend string) bool {
	switch backend {
	case "openrouter", "openai", "google":
		return true
	case "codex", "gemini":
		return false
	}
	return true // unknown — fail safe
}

func backendEnvName(cfg config.Config) string {
	switch cfg.Backend {
	case "openrouter":
		return cfg.OpenRouter.APIKeyEnv
	case "openai":
		return cfg.OpenAI.APIKeyEnv
	case "google":
		return cfg.Google.APIKeyEnv
	case "codex":
		return cfg.Codex.APIKeyEnv
	case "gemini":
		return cfg.Gemini.APIKeyEnv
	}
	return ""
}

func indexOf(slice []string, s string) int {
	for i, v := range slice {
		if v == s {
			return i
		}
	}
	return -1
}

func configFileExists(path string) bool {
	_, err := os.Stat(path)
	return !errors.Is(err, fs.ErrNotExist)
}

// ---- prompter ----

type prompter struct {
	sc  *bufio.Scanner
	out io.Writer
}

func newPrompter(in io.Reader, out io.Writer) *prompter {
	return &prompter{sc: bufio.NewScanner(in), out: out}
}

// ask prompts question with an optional default. Empty input or EOF
// returns defaultValue.
func (p *prompter) ask(question, defaultValue string) string {
	if defaultValue != "" {
		fmt.Fprintf(p.out, "%s [%s]: ", question, defaultValue)
	} else {
		fmt.Fprintf(p.out, "%s: ", question)
	}
	if !p.sc.Scan() {
		fmt.Fprintln(p.out)
		return defaultValue
	}
	text := strings.TrimSpace(p.sc.Text())
	if text == "" {
		return defaultValue
	}
	return text
}

// askChoice prompts for a numbered enum. Reprompts up to maxRetries on
// invalid input; falls through to defaultIdx if input is exhausted.
func (p *prompter) askChoice(question string, options []string, defaultIdx int) int {
	const maxRetries = 3
	var prompt strings.Builder
	prompt.WriteString(question)
	prompt.WriteString(" (")
	for i, opt := range options {
		if i > 0 {
			prompt.WriteString(", ")
		}
		fmt.Fprintf(&prompt, "%d=%s", i+1, opt)
	}
	prompt.WriteString(")")

	for attempt := 0; attempt < maxRetries; attempt++ {
		defaultStr := ""
		if defaultIdx >= 0 && defaultIdx < len(options) {
			defaultStr = strconv.Itoa(defaultIdx + 1)
		}
		ans := p.ask(prompt.String(), defaultStr)
		n, err := strconv.Atoi(ans)
		if err == nil && n >= 1 && n <= len(options) {
			return n - 1
		}
		fmt.Fprintf(p.out, "Please enter a number between 1 and %d.\n", len(options))
	}
	return defaultIdx
}

// askEnvVarName prompts for an environment variable name and rejects
// inputs that look like API key values (or otherwise violate POSIX
// portable env-var-name rules: starts with letter or _, then letters,
// digits, or _, max 256 chars). On invalid input the user gets a clear
// message — the typical confusion is typing the key VALUE at this prompt
// — and is reprompted up to maxRetries times. After exhaustion the
// function falls through to defaultValue rather than infinite-looping.
func (p *prompter) askEnvVarName(question, defaultValue string) string {
	const maxRetries = 3
	for attempt := 0; attempt < maxRetries; attempt++ {
		ans := p.ask(question, defaultValue)
		if validEnvVarName(ans) {
			return ans
		}
		fmt.Fprintln(p.out, "That doesn't look like an environment variable name (expected letters, digits,")
		fmt.Fprintln(p.out, "and underscores; e.g. OPENROUTER_API_KEY). If you typed the key value, set it")
		fmt.Fprintln(p.out, "in your shell with `export NAME=value` and enter just the NAME here.")
	}
	return defaultValue
}

// validEnvVarName returns true if s satisfies POSIX portable rules for
// environment variable names: non-empty, ≤256 bytes, begins with letter
// or underscore, all subsequent runes are letters, digits, or underscore.
// Hyphens (the dead giveaway for an OAuth-style key value like
// "sk-or-v1-...") are rejected.
func validEnvVarName(s string) bool {
	if s == "" || len(s) > 256 {
		return false
	}
	for i, r := range s {
		switch {
		case r == '_':
		case r >= 'A' && r <= 'Z':
		case r >= 'a' && r <= 'z':
		case i > 0 && r >= '0' && r <= '9':
		default:
			return false
		}
	}
	return true
}

// askYesNo prompts a yes/no question. Empty input returns the default;
// invalid input also returns the default (no reprompt — keeps tests sane).
func (p *prompter) askYesNo(question string, defaultYes bool) bool {
	suffix := "[y/N]"
	if defaultYes {
		suffix = "[Y/n]"
	}
	ans := p.ask(question+" "+suffix, "")
	if ans == "" {
		return defaultYes
	}
	switch strings.ToLower(ans) {
	case "y", "yes":
		return true
	case "n", "no":
		return false
	}
	return defaultYes
}
