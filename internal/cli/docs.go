package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/lernen-edu/lernen/internal/config"
	"github.com/lernen-edu/lernen/internal/docs"
	"github.com/lernen-edu/lernen/internal/docs/caching"
	"github.com/lernen-edu/lernen/internal/docs/context7"
	"github.com/lernen-edu/lernen/internal/docs/null"
)

// docsDefaultMaxTokens is the maxTokens value passed to QueryDocs when the
// user runs `lernen docs <library> <topic>` without further configuration.
// 4096 fits comfortably inside any current model's context budget while
// returning enough text to be useful at the terminal.
const docsDefaultMaxTokens = 4096

// DocsDeps is the dependency-injection surface for `lernen docs`.
type DocsDeps struct {
	// DocsProviderFactory constructs the docs provider selected by the
	// loaded config. Production wires productionDocsProvider; tests pass
	// a fake.
	DocsProviderFactory func(*config.Config) (docs.Provider, error)

	// Out is where the fetched doc body is written. Production sets this
	// to os.Stdout; tests capture it via *bytes.Buffer.
	Out io.Writer

	// ConfigPath optionally overrides the default ConfigFile() lookup.
	// Tests usually point this at a path that does not exist so
	// config.Load returns Default().
	ConfigPath string
}

// ProductionDocsDeps returns the DocsDeps wired for the shipped binary.
func ProductionDocsDeps() DocsDeps {
	return DocsDeps{
		DocsProviderFactory: productionDocsProvider,
		Out:                 os.Stdout,
	}
}

// NewDocsCmd builds the `lernen docs` Cobra command.
//
// Usage:
//
//	lernen docs <library> <topic>
//
// Resolves <library> against the configured docs provider, fetches text
// for <topic>, and writes it to stdout. Returns a non-zero exit code on
// any failure (provider unconfigured, library not found, upstream error).
func NewDocsCmd(deps DocsDeps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "docs <library> <topic>",
		Short: "Look up documentation for a library and topic.",
		Long: `Look up documentation directly via the configured docs provider.

Resolves <library> (e.g. "react", "pytest") to a provider-specific
identifier, then fetches text for <topic> (e.g. "useState", "fixtures")
and writes it to stdout. Useful as a sanity check that the docs stack
is configured correctly, and as a standalone reference lookup.

Configuration: docs_provider in ~/.config/lernen/config.toml selects
the provider. "null" (default) returns no docs; "context7" fetches
from Context7 with a local SQLite cache.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDocs(cmd.Context(), args[0], args[1], deps)
		},
	}
	// main() in cmd/lernen prints the returned error; cobra's automatic
	// "Error: ..." + usage dump on top of that produces a triple-printed
	// error. Silence both so we get exactly one print, on stderr.
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	return cmd
}

// runDocs performs the resolve-then-query flow. Errors are wrapped to
// identify which step failed.
func runDocs(ctx context.Context, library, topic string, deps DocsDeps) error {
	if deps.DocsProviderFactory == nil {
		return errors.New("docs: DocsProviderFactory is nil (programmer error)")
	}
	if deps.Out == nil {
		return errors.New("docs: Out is nil (programmer error)")
	}

	cfgPath := deps.ConfigPath
	if cfgPath == "" {
		p, err := ConfigFile()
		if err != nil {
			return fmt.Errorf("docs: resolve config path: %w", err)
		}
		cfgPath = p
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}

	provider, err := deps.DocsProviderFactory(&cfg)
	if err != nil {
		return fmt.Errorf("docs: construct provider: %w", err)
	}
	if closer, ok := provider.(io.Closer); ok {
		defer closer.Close()
	}

	libID, err := provider.ResolveLibrary(ctx, library)
	if err != nil {
		if errors.Is(err, docs.ErrNotConfigured) {
			return fmt.Errorf("docs: provider is not configured — set the docs API-key env var, or change docs_provider in %s: %w", cfgPath, err)
		}
		return fmt.Errorf("docs: resolve %q: %w", library, err)
	}

	body, err := provider.QueryDocs(ctx, libID, topic, docsDefaultMaxTokens)
	if err != nil {
		return fmt.Errorf("docs: query %q in %q: %w", topic, libID, err)
	}

	fmt.Fprintln(deps.Out, body)
	return nil
}

// productionDocsProvider constructs the docs provider selected by the
// loaded config. Caching is always-on for context7 with default 30-day
// TTL; the user opts out by setting docs_provider = "null".
func productionDocsProvider(cfg *config.Config) (docs.Provider, error) {
	switch cfg.DocsProvider {
	case "null", "":
		return null.New(), nil
	case "context7":
		inner := context7.New(context7.Config{APIKeyEnv: cfg.Context7.APIKeyEnv})
		return caching.New(inner, caching.Options{})
	default:
		return nil, fmt.Errorf("docs: unknown docs_provider %q", cfg.DocsProvider)
	}
}
