// Package config loads golem's layered TOML configuration (docs/CONFIG.md).
//
// Precedence, low → high: built-in defaults, the global file, the project
// file, GOLEM_* environment overrides. Tables deep-merge; slices replace.
// The project-level .golem.toml is committable and rejects plaintext keys
// (ADR-0015).
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// DefaultProviderID matches docs/CONFIG.md [provider].default.
const DefaultProviderID = "anthropic"

// DefaultPluginTimeoutSecs is the per-tool-call timeout an MCP plugin uses
// when [plugins.mcp.<name>] timeout_secs is absent (docs/CONFIG.md).
const DefaultPluginTimeoutSecs = 30

// ProviderSettings is one [provider.<id>] table.
type ProviderSettings struct {
	Model   string
	APIKey  string // raw value; "ref:ENV_VAR" resolves via ResolveKey
	BaseURL string
}

// PluginSettings is one [plugins.mcp.<name>] table (docs/CONFIG.md). Env
// values keep their raw form — "ref:ENV_VAR" resolves at plugin launch.
type PluginSettings struct {
	Command     []string
	Env         map[string]string
	Disabled    bool
	TimeoutSecs int
}

// Config is the merged configuration.
type Config struct {
	// Path is the highest layer that existed ("" when none did).
	Path string
	// Default is provider.default after the GOLEM_PROVIDER override.
	Default string
	// Providers holds the per-id [provider.<id>] tables.
	Providers map[string]ProviderSettings
	// Session holds the [session] table.
	Session SessionSettings
	// Plugins holds the per-name [plugins.mcp.<name>] tables.
	Plugins map[string]PluginSettings
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

// ProjectPath is the per-project config file inside dir.
func ProjectPath(dir string) string {
	return filepath.Join(dir, ".golem.toml")
}

// Load reads a single config file, applying built-in defaults and the
// GOLEM_PROVIDER override. A missing file is not an error: defaults apply.
// A file that fails to parse aborts startup with the path in the error.
func Load(path string) (*Config, error) {
	return LoadChain(path, "")
}

// LoadChain merges the config layers, low → high: built-in defaults, the
// global file, the project file, then GOLEM_PROVIDER. Missing files are
// skipped; a file that fails to parse (or violates the project-file secret
// rule, ADR-0015) aborts with the file path in the error. Map values
// deep-merge across layers; slices replace.
func LoadChain(global, project string) (*Config, error) {
	globalDoc, hasGlobal, err := parseDoc(global)
	if err != nil {
		return nil, err
	}

	projectDoc, hasProject, err := parseDoc(project)
	if err != nil {
		return nil, err
	}

	if hasProject {
		if err := rejectPlaintextKeys(projectDoc, project); err != nil {
			return nil, err
		}
	}

	merged := map[string]any{}
	mergeDoc(merged, globalDoc)
	mergeDoc(merged, projectDoc)

	plugins, err := parsePlugins(merged)
	if err != nil {
		return nil, err
	}

	cfg := &Config{
		Default:   DefaultProviderID,
		Providers: map[string]ProviderSettings{},
		Plugins:   plugins,
	}

	if hasProject {
		cfg.Path = project
	} else if hasGlobal {
		cfg.Path = global
	}

	defaultID, providers := extractProviders(merged)
	cfg.Providers = providers
	cfg.Session = parseSession(merged)

	if defaultID != "" {
		cfg.Default = defaultID
	}

	if env := os.Getenv("GOLEM_PROVIDER"); env != "" {
		cfg.Default = env
	}

	return cfg, nil
}

// extractProviders decodes the provider tables from a merged document:
// the provider.default value and the per-id [provider.<id>] settings.
func extractProviders(doc map[string]any) (defaultID string, providers map[string]ProviderSettings) {
	providerTable, ok := doc["provider"].(map[string]any)
	if !ok {
		return "", map[string]ProviderSettings{}
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

	return defaultID, providers
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

// ErrPlaintextKey marks a literal api_key in the project-level config —
// that file is committable and must indirect secrets through ref:
// (ADR-0015).
var ErrPlaintextKey = errors.New("plaintext api_key in project config")

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
