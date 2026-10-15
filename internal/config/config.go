// Package config loads golem's layered TOML configuration (docs/CONFIG.md).
//
// This bootstrap implements the provider slice of the precedence chain —
// built-in defaults, the global config file, and GOLEM_* environment
// overrides. Project files and CLI flags arrive with roadmap phase 5.
package config

import (
	"errors"
	"fmt"
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
	// Session holds the [session] table.
	Session SessionSettings
}

// SessionSettings is the [session] table.
type SessionSettings struct {
	// AutoCompactBytes is the folded-history byte threshold triggering an
	// automatic compaction before a turn starts (ADR-0025). 0 disables
	// automatic compaction. Absent falls back to the default.
	AutoCompactBytes int
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

	defaultID, providers, doc, err := parseProviders(string(raw), path)
	if err != nil {
		return nil, err
	}

	cfg.Path = path
	cfg.Providers = providers
	cfg.Session = parseSession(doc)

	if defaultID != "" {
		cfg.Default = defaultID
	}

	if env := os.Getenv("GOLEM_PROVIDER"); env != "" {
		cfg.Default = env
	}

	return cfg, nil
}

// parseProviders decodes the file body into the provider.default value and
// the per-id [provider.<id>] settings. The parsed document is returned for
// follow-on table extraction (the [session] table).
func parseProviders(
	body, path string,
) (defaultID string, providers map[string]ProviderSettings, doc map[string]any, err error) {
	if err = toml.Unmarshal([]byte(body), &doc); err != nil {
		return "", nil, nil, fmt.Errorf("config: parsing %s: %w", path, err)
	}

	providerTable, ok := doc["provider"].(map[string]any)
	if !ok {
		return "", map[string]ProviderSettings{}, doc, nil
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

	return defaultID, providers, doc, nil
}

// parseSession extracts the [session] table's settings. Absent or
// negative values yield 0 (auto compaction off — the caller decides the
// default), matching docs/CONFIG.md.
func parseSession(doc map[string]any) SessionSettings {
	var settings SessionSettings

	table, ok := doc["session"].(map[string]any)
	if !ok {
		return settings
	}

	if value, ok := table["auto_compact_bytes"].(int64); ok && value >= 0 {
		settings.AutoCompactBytes = int(value)
	}

	return settings
}

// stringValue coerces a TOML value to a string ("" when absent or not a
// string).
func stringValue(v any) string {
	s, _ := v.(string)

	return s
}

// ErrKeyUnresolved marks a ref: indirection whose environment variable is
// not set; serve treats it as degraded, not fatal.
var ErrKeyUnresolved = errors.New("api key unresolved")

// ResolveKey expands an api_key value. "ref:NAME" resolves from the
// environment — a missing NAME is an error naming it (docs/CONFIG.md
// Secrets). Plaintext keys are the designed default for the global config
// (ADR-0015: single-user local tool, 0600 file, the gh/aws-cli precedent).
func ResolveKey(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}

	if name, ok := strings.CutPrefix(raw, "ref:"); ok {
		value := os.Getenv(name)
		if value == "" {
			return "", fmt.Errorf("config: %w: ref %s: environment variable not set", ErrKeyUnresolved, name)
		}

		return value, nil
	}

	return raw, nil
}
