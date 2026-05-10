//go:build !windows

package cli

import (
	"fmt"
	"os"
	"path/filepath"
)

// configRoot returns the lernen-specific config directory on Linux/macOS.
// Honors XDG_CONFIG_HOME if set, otherwise defaults to $HOME/.config.
func configRoot() (string, error) {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, lernenSubdir), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("paths: resolve home directory: %w", err)
	}
	return filepath.Join(home, ".config", lernenSubdir), nil
}

// dataRoot returns the lernen-specific data directory on Linux/macOS.
// Honors XDG_DATA_HOME if set, otherwise defaults to $HOME/.local/share.
func dataRoot() (string, error) {
	if dir := os.Getenv("XDG_DATA_HOME"); dir != "" {
		return filepath.Join(dir, lernenSubdir), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("paths: resolve home directory: %w", err)
	}
	return filepath.Join(home, ".local", "share", lernenSubdir), nil
}

// stateRoot returns the lernen-specific state directory on Linux/macOS.
// Honors XDG_STATE_HOME if set, otherwise defaults to $HOME/.local/state.
func stateRoot() (string, error) {
	if dir := os.Getenv("XDG_STATE_HOME"); dir != "" {
		return filepath.Join(dir, lernenSubdir), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("paths: resolve home directory: %w", err)
	}
	return filepath.Join(home, ".local", "state", lernenSubdir), nil
}
