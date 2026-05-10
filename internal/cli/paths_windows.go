//go:build windows

package cli

import (
	"fmt"
	"os"
	"path/filepath"
)

// configRoot returns the lernen-specific config directory on Windows.
// Uses %APPDATA% (the roaming user data directory).
func configRoot() (string, error) {
	dir := os.Getenv("APPDATA")
	if dir == "" {
		return "", fmt.Errorf("paths: APPDATA environment variable is not set")
	}
	return filepath.Join(dir, lernenSubdir), nil
}

// dataRoot returns the lernen-specific data directory on Windows.
// Uses %LOCALAPPDATA% (the per-machine, non-roaming user data directory).
func dataRoot() (string, error) {
	dir := os.Getenv("LOCALAPPDATA")
	if dir == "" {
		return "", fmt.Errorf("paths: LOCALAPPDATA environment variable is not set")
	}
	return filepath.Join(dir, lernenSubdir), nil
}

// stateRoot returns the lernen-specific state directory on Windows.
// Windows has no separate state-vs-data convention; both share %LOCALAPPDATA%.
func stateRoot() (string, error) {
	return dataRoot()
}
