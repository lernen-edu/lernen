// Command lernen is the entry point for the Lernen CLI.
//
// See AGENTS.md and docs/PRD.md for project context.
package main

import (
	"fmt"
	"os"

	"github.com/lernen-edu/lernen/internal/cli"
	logpkg "github.com/lernen-edu/lernen/internal/log"

	// Blank-import language adapters so their init() registrations run
	// in the shipped binary. Without this, languages.Get returns no
	// adapters even though the packages compile. M3: replace with an
	// internal/languages/all aggregator when adapter count grows.
	_ "github.com/lernen-edu/lernen/internal/languages/python"
)

func main() {
	if err := cli.Execute(); err != nil {
		fmt.Fprintln(logpkg.NewRedactor().Writer(os.Stderr), err)
		os.Exit(1)
	}
}
