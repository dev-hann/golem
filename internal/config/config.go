// Package config loads golem's layered TOML configuration (docs/CONFIG.md).
//
// This bootstrap implements the provider slice of the precedence chain —
// built-in defaults, the global config file, and GOLEM_* environment
// overrides. Project files and CLI flags arrive with roadmap phase 5.
package config

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// DefaultProviderID matches docs/CONFIG.md [provider].default.
const DefaultProviderID = "anthropic"

// ProviderSettings is one [provider.<id>] table.
type ProviderSettings struct {
	Model   string
	APIKey  string // raw value; "ref:ENV_VAR" resolves via ResolveKey
	BaseURL string
}

// Config is the merged provider configuration.
type Config struct {
	// Path is the file settings were loaded from ("" when none existed).
	Path string
	// Default is provider.default after the GOLEM_PROVIDER override.
	Default string
	// Providers holds the per-id [provider.<id>] tables.
	Providers map[string]ProviderSettings
}

// DefaultPath is the global config file location, honoring XDG_CONFIG_HOME.
func DefaultPath() string {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "golem", "config.toml")
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "golem/config.toml"
	}

	return filepath.Join(home, ".config", "golem", "config.toml")
}

// Load reads the config file at path, applying built-in defaults and the
// GOLEM_PROVIDER override. A missing file is not an error: defaults apply.
// A file that fails to parse aborts startup with the path in the error.
func Load(path string) (*Config, error) {
	cfg := &Config{Default: DefaultProviderID, Providers: map[string]ProviderSettings{}}

	raw, err := os.ReadFile(path) //nolint:gosec // golem's own config path, not user-supplied input
	if errors.Is(err, fs.ErrNotExist) {
		if env := os.Getenv("GOLEM_PROVIDER"); env != "" {
			cfg.Default = env
		}

		return cfg, nil
	}

	if err != nil {
		return nil, fmt.Errorf("config: reading %s: %w", path, err)
	}

	defaultID, providers, err := parseProviders(string(raw), path)
	if err != nil {
		return nil, err
	}

	cfg.Path = path
	cfg.Providers = providers

	if defaultID != "" {
		cfg.Default = defaultID
	}

	if env := os.Getenv("GOLEM_PROVIDER"); env != "" {
		cfg.Default = env
	}

	return cfg, nil
}

// parseProviders decodes the file body into the provider.default value and
// the per-id [provider.<id>] settings.
func parseProviders(body, path string) (defaultID string, providers map[string]ProviderSettings, err error) {
	var doc map[string]any

	if err = toml.Unmarshal([]byte(body), &doc); err != nil {
		return "", nil, fmt.Errorf("config: parsing %s: %w", path, err)
	}

	providerTable, ok := doc["provider"].(map[string]any)
	if !ok {
		return "", map[string]ProviderSettings{}, nil
	}

	defaultID, _ = providerTable["default"].(string)
	providers = map[string]ProviderSettings{}

	for id, val := range providerTable {
		table, ok := val.(map[string]any)
		if !ok {
			continue // scalar keys such as provider.default
		}

		providers[id] = ProviderSettings{
			Model:   stringValue(table["model"]),
			APIKey:  stringValue(table["api_key"]),
			BaseURL: stringValue(table["base_url"]),
		}
	}

	return defaultID, providers, nil
}

// stringValue coerces a TOML value to a string ("" when absent or not a
// string).
func stringValue(v any) string {
	s, _ := v.(string)

	return s
}

// ResolveKey expands an api_key value. "ref:NAME" resolves from the
// environment — a missing NAME is an error naming it (docs/CONFIG.md
// Secrets). Plaintext passes through with a warning, for testing only.
func ResolveKey(raw string, warn io.Writer) (string, error) {
	if raw == "" {
		return "", nil
	}

	if name, ok := strings.CutPrefix(raw, "ref:"); ok {
		value := os.Getenv(name)
		if value == "" {
			return "", fmt.Errorf("config: api_key ref %s: environment variable not set", name)
		}

		return value, nil
	}

	if warn != nil {
		fmt.Fprintf(warn, "warning: plaintext api_key in config — use ref:ENV_VAR\n") //nolint:errcheck // terminal output
	}

	return raw, nil
}
