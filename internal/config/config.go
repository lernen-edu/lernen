// Package config loads and validates the lernen TOML configuration file.
//
// Lernen reads a single TOML file (default ~/.config/lernen/config.toml on
// Linux/macOS, %APPDATA%\lernen\config.toml on Windows) at process start. The
// file selects the active inference backend, names the docs provider, and
// carries backend-specific settings.
//
// See docs/PRD.md §4.1 for the configuration schema and docs/PRE_BUILD_ANSWERS.md
// §8 for the read-once-at-startup decision.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	toml "github.com/pelletier/go-toml/v2"
)

// Config is the parsed lernen configuration. Zero values are not safe to use
// directly; call Default() to obtain a populated baseline, then merge with
// Load() against a TOML file.
type Config struct {
	// Backend selects the active inference backend.
	// Valid values: "openrouter", "codex", "gemini", "fake".
	Backend string `toml:"backend"`

	// DocsProvider selects the documentation provider used by the runtime
	// tutor for grounding library-specific answers.
	// Valid values: "null" (no docs), "context7" (M2 and later).
	DocsProvider string `toml:"docs_provider"`

	OpenRouter OpenRouterConfig `toml:"openrouter"`
	OpenAI     OpenAIConfig     `toml:"openai"`
	Google     GoogleConfig     `toml:"google"`
	Codex      CodexConfig      `toml:"codex"`
	Gemini     GeminiConfig     `toml:"gemini"`
	Context7   Context7Config   `toml:"context7"`
}

// GoogleConfig carries settings for the Google Gemini direct HTTP backend (M2.5).
// Use this when the user has a Gemini API key from aistudio.google.com and
// does not want to install gemini-cli. Users who want gemini-cli's auth
// state (free Cloud Code Assist tier, Vertex ADC) should use the gemini
// backend, which delegates to the gemini binary.
type GoogleConfig struct {
	// APIKeyEnv names the environment variable holding the Gemini API key.
	// Default "GEMINI_API_KEY".
	APIKeyEnv string `toml:"api_key_env"`

	// Model is the Gemini model identifier (e.g., "gemini-2.5-flash").
	Model string `toml:"model"`
}

// OpenAIConfig carries settings for the OpenAI direct HTTP backend (M2.5).
// Use this when the user has an OpenAI API key from platform.openai.com
// and does not want to install the codex CLI. Users who want their
// ChatGPT Plus subscription credits to cover API calls should use the
// codex backend, which delegates to the codex CLI's own auth.
type OpenAIConfig struct {
	// APIKeyEnv names the environment variable holding the OpenAI API key.
	// Default "OPENAI_API_KEY".
	APIKeyEnv string `toml:"api_key_env"`

	// Model is the OpenAI model identifier (e.g., "gpt-5.4").
	Model string `toml:"model"`
}

// OpenRouterConfig carries settings for the OpenRouter HTTP backend.
// See docs/PRE_BUILD_ANSWERS.md §4 for header and SSE details.
type OpenRouterConfig struct {
	// APIKeyEnv names the environment variable holding the OpenRouter API key.
	// Default "OPENROUTER_API_KEY".
	APIKeyEnv string `toml:"api_key_env"`

	// Model is the OpenRouter model identifier (e.g., "openai/gpt-5.2").
	Model string `toml:"model"`
}

// CodexConfig carries settings for the Codex CLI subprocess backend (M2).
type CodexConfig struct {
	// Binary is the executable name resolved against PATH. Default "codex".
	Binary string `toml:"binary"`

	// APIKeyEnv names the environment variable holding the Codex API key.
	// Default "CODEX_API_KEY".
	APIKeyEnv string `toml:"api_key_env"`

	// Model is the codex `-m` flag value (e.g. "gpt-5.4").
	Model string `toml:"model"`
}

// GeminiConfig carries settings for the Gemini CLI subprocess backend (M2).
type GeminiConfig struct {
	// Binary is the executable name resolved against PATH. Default "gemini".
	Binary string `toml:"binary"`

	// APIKeyEnv names the environment variable holding the Gemini API key.
	// Default "GEMINI_API_KEY".
	APIKeyEnv string `toml:"api_key_env"`

	// Model is the gemini `-m` flag value (e.g. "gemini-2.5-flash").
	Model string `toml:"model"`
}

// Context7Config carries settings for the Context7 docs provider (M2).
type Context7Config struct {
	// APIKeyEnv names the environment variable holding the Context7 API key.
	// Default "CONTEXT7_API_KEY".
	APIKeyEnv string `toml:"api_key_env"`
}

// Default returns a Config populated with sane defaults. Useful both as the
// starting point for Load() and as the value returned when no config file
// exists on disk.
func Default() Config {
	return Config{
		Backend:      "openrouter",
		DocsProvider: "null",
		OpenRouter: OpenRouterConfig{
			APIKeyEnv: "OPENROUTER_API_KEY",
			Model:     "qwen/qwen-2.5-coder-32b-instruct",
		},
		OpenAI: OpenAIConfig{
			APIKeyEnv: "OPENAI_API_KEY",
			Model:     "gpt-5.4",
		},
		Google: GoogleConfig{
			APIKeyEnv: "GEMINI_API_KEY",
			Model:     "gemini-2.5-flash",
		},
		Codex: CodexConfig{
			Binary:    "codex",
			APIKeyEnv: "CODEX_API_KEY",
			Model:     "gpt-5.4",
		},
		Gemini: GeminiConfig{
			Binary:    "gemini",
			APIKeyEnv: "GEMINI_API_KEY",
			Model:     "gemini-2.5-flash",
		},
		Context7: Context7Config{APIKeyEnv: "CONTEXT7_API_KEY"},
	}
}

// Load reads the TOML configuration at path, merges it onto Default(), and
// validates the result. If path does not exist, Load returns Default() and a
// nil error. If path exists but cannot be read or parsed, Load returns the
// (possibly partial) config and a wrapped error suitable for surfacing to the
// user.
func Load(path string) (Config, error) {
	cfg := Default()

	f, err := os.Open(path) // #nosec G304 -- path is supplied by the operator
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return cfg, nil
		}
		return cfg, fmt.Errorf("config: open %s: %w", path, err)
	}
	defer f.Close()

	dec := toml.NewDecoder(f)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return cfg, fmt.Errorf("config: parse %s: %w", path, err)
	}

	if err := cfg.Validate(); err != nil {
		return cfg, fmt.Errorf("config: %s: %w", path, err)
	}
	return cfg, nil
}

// Save marshals cfg to TOML and writes it atomically to path. The parent
// directory is created with mode 0700 if missing; the file is written with
// mode 0600. Atomic write protects against half-written files when the
// process is interrupted: we write to a temp file in the same directory
// and rename it into place.
func Save(cfg Config, path string) error {
	body, err := toml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("config: marshal: %w", err)
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("config: create dir %s: %w", dir, err)
	}

	tmp, err := os.CreateTemp(dir, "config.toml.*.tmp")
	if err != nil {
		return fmt.Errorf("config: create temp: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("config: write %s: %w", tmpPath, err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("config: chmod %s: %w", tmpPath, err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("config: close %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("config: rename %s -> %s: %w", tmpPath, path, err)
	}
	return nil
}

// Validate checks that Backend and DocsProvider hold supported values and that
// backend-specific settings are coherent. Returns nil if the config is usable.
func (c Config) Validate() error {
	switch c.Backend {
	case "openrouter", "openai", "google", "codex", "gemini", "fake":
	case "":
		return errors.New("backend is required")
	default:
		return fmt.Errorf("backend %q is not supported (want one of: openrouter, openai, google, codex, gemini, fake)", c.Backend)
	}

	switch c.DocsProvider {
	case "null", "context7":
	case "":
		return errors.New("docs_provider is required")
	default:
		return fmt.Errorf("docs_provider %q is not supported (want one of: null, context7)", c.DocsProvider)
	}

	if c.Backend == "openrouter" {
		if c.OpenRouter.APIKeyEnv == "" {
			return errors.New("[openrouter].api_key_env is required when backend = \"openrouter\"")
		}
		if c.OpenRouter.Model == "" {
			return errors.New("[openrouter].model is required when backend = \"openrouter\"")
		}
	}
	if c.Backend == "openai" {
		if c.OpenAI.APIKeyEnv == "" {
			return errors.New("[openai].api_key_env is required when backend = \"openai\"")
		}
		if c.OpenAI.Model == "" {
			return errors.New("[openai].model is required when backend = \"openai\"")
		}
	}
	if c.Backend == "google" {
		if c.Google.APIKeyEnv == "" {
			return errors.New("[google].api_key_env is required when backend = \"google\"")
		}
		if c.Google.Model == "" {
			return errors.New("[google].model is required when backend = \"google\"")
		}
	}
	return nil
}
