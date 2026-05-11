package cli

import (
	"fmt"
	"os"
	"path/filepath"
)

// lernenSubdir is the directory name appended to platform-specific roots
// to keep lernen's data namespaced.
const lernenSubdir = "lernen"

// dirMode is the permission used when MkdirAll creates a missing directory.
// Honors typical umask; not security-sensitive on its own — credentials live
// in env vars and the OS-managed home directory tree.
const dirMode = 0o755

// ConfigDir returns the directory where lernen reads its TOML config file.
// On Linux/macOS this honors $XDG_CONFIG_HOME, defaulting to ~/.config/lernen;
// on Windows it uses %APPDATA%\lernen.
//
// The directory is created if it does not exist.
func ConfigDir() (string, error) {
	return ensureDir(configRoot)
}

// DataDir returns the directory where lernen stores user-owned data
// (curriculum manifests, the SQLite state database). On Linux/macOS this
// honors $XDG_DATA_HOME, defaulting to ~/.local/share/lernen; on Windows
// it uses %LOCALAPPDATA%\lernen.
//
// The directory is created if it does not exist.
func DataDir() (string, error) {
	return ensureDir(dataRoot)
}

// StateDir returns the directory where lernen writes log files. On Linux/macOS
// this honors $XDG_STATE_HOME, defaulting to ~/.local/state/lernen; on Windows
// it shares %LOCALAPPDATA%\lernen with DataDir (Windows has no separate
// state-vs-data convention).
//
// The directory is created if it does not exist.
func StateDir() (string, error) {
	return ensureDir(stateRoot)
}

// ConfigFile returns the absolute path to the lernen TOML configuration file
// (i.e., ConfigDir() + "/config.toml"). It does not create the file.
func ConfigFile() (string, error) {
	dir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.toml"), nil
}

// ManifestsDir returns the directory where forge-generated curriculum
// manifests live (i.e., DataDir() + "/manifests"). It is created if missing.
func ManifestsDir() (string, error) {
	dir, err := DataDir()
	if err != nil {
		return "", err
	}
	p := filepath.Join(dir, "manifests")
	if err := os.MkdirAll(p, dirMode); err != nil {
		return "", fmt.Errorf("paths: mkdir %s: %w", p, err)
	}
	return p, nil
}

// ProfileDir returns the directory where lernen stores user-owned profile
// data — goals.yaml from forge Stage 0, plus per-stage outputs from
// later sub-projects (starting_point.yaml, recommendation.yaml, ...).
// On Linux/macOS this is DataDir()/profile (i.e., ~/.local/share/lernen/profile
// under default XDG_DATA_HOME); on Windows it is %LOCALAPPDATA%\lernen\profile.
//
// The directory is created if it does not exist.
func ProfileDir() (string, error) {
	dir, err := DataDir()
	if err != nil {
		return "", err
	}
	p := filepath.Join(dir, "profile")
	if err := os.MkdirAll(p, dirMode); err != nil {
		return "", fmt.Errorf("paths: mkdir %s: %w", p, err)
	}
	return p, nil
}

// ProgressDir returns the directory where lernen stores per-curriculum
// runtime user state — current chapter pointer plus completed-chapter
// records. Sibling to ManifestsDir() and ProfileDir(): on Linux/macOS
// this is DataDir()/progress (i.e., ~/.local/share/lernen/progress
// under default XDG_DATA_HOME); on Windows it is
// %LOCALAPPDATA%\lernen\progress.
//
// The directory is created if it does not exist.
func ProgressDir() (string, error) {
	dir, err := DataDir()
	if err != nil {
		return "", err
	}
	p := filepath.Join(dir, "progress")
	if err := os.MkdirAll(p, dirMode); err != nil {
		return "", fmt.Errorf("paths: mkdir %s: %w", p, err)
	}
	return p, nil
}

// ensureDir resolves a platform-specific root via the supplied function and
// ensures the directory exists.
func ensureDir(resolve func() (string, error)) (string, error) {
	p, err := resolve()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(p, dirMode); err != nil {
		return "", fmt.Errorf("paths: mkdir %s: %w", p, err)
	}
	return p, nil
}
