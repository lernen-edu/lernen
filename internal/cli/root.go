// Package cli wires the lernen CLI's subcommands together.
package cli

import (
	"fmt"
	"runtime/debug"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "lernen",
	Short: "Learn to think before you prompt.",
	Long: `Lernen takes a vibe coder and turns them into an AI-augmented engineer.
Phase 1 builds fluency in one language with the AI tutor structurally
prevented from writing code. Phase 2 teaches the user to direct, evaluate,
and threat-model real agentic coding tools.`,
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the lernen version.",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println(versionString())
	},
}

// Execute runs the root command. It is the single entry point from main.
func Execute() error {
	rootCmd.AddCommand(versionCmd)
	rootCmd.AddCommand(NewWorkCmd(ProductionWorkDeps()))
	rootCmd.AddCommand(NewForgeCmd(ProductionForgeDeps()))
	rootCmd.AddCommand(NewDocsCmd(ProductionDocsDeps()))
	rootCmd.AddCommand(NewSetupCmd(ProductionSetupDeps()))
	rootCmd.AddCommand(NewPracticeCmd(ProductionPracticeDeps()))
	rootCmd.AddCommand(NewStatusCmd(ProductionStatusDeps()))
	rootCmd.AddCommand(NewGateCmd(ProductionGateDeps()))
	return rootCmd.Execute()
}

func versionString() string {
	const fallback = "lernen development build"
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return fallback
	}
	for _, s := range bi.Settings {
		if s.Key == "vcs.revision" && len(s.Value) >= 7 {
			return fmt.Sprintf("lernen development build (%s)", s.Value[:7])
		}
	}
	return fallback
}
