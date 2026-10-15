package config

import "fmt"

// parsePlugins extracts the [plugins.mcp.<name>] tables from a merged
// document with schema validation (docs/CONFIG.md): command is a required
// non-empty argv of strings. Values stay raw — "ref:ENV_VAR" env entries
// and timeouts resolve at plugin launch (phase 6).
func parsePlugins(doc map[string]any) (map[string]PluginSettings, error) {
	plugins := map[string]PluginSettings{}

	pluginsTable, ok := doc["plugins"].(map[string]any)
	if !ok {
		return plugins, nil
	}

	mcpTable, ok := pluginsTable["mcp"].(map[string]any)
	if !ok {
		return plugins, nil
	}

	for name, val := range mcpTable {
		table, ok := val.(map[string]any)
		if !ok {
			continue // scalar keys under [plugins.mcp] are not plugins
		}

		settings, err := parsePlugin(name, table)
		if err != nil {
			return nil, err
		}

		plugins[name] = settings
	}

	return plugins, nil
}

// parsePlugin validates one [plugins.mcp.<name>] table.
func parsePlugin(name string, table map[string]any) (PluginSettings, error) {
	var settings PluginSettings

	command, err := stringSlice(table["command"])
	if err != nil || len(command) == 0 {
		return settings, fmt.Errorf(
			"config: [plugins.mcp.%s] command: required non-empty argv of strings",
			name,
		)
	}

	settings.Command = command

	if envTable, ok := table["env"].(map[string]any); ok {
		settings.Env = make(map[string]string, len(envTable))

		for key, val := range envTable {
			settings.Env[key] = stringValue(val)
		}
	}

	settings.Disabled, _ = table["disabled"].(bool)

	if secs, ok := table["timeout_secs"].(int64); ok && secs > 0 {
		settings.TimeoutSecs = int(secs)
	}

	return settings, nil
}

// stringSlice coerces a TOML array to []string; non-string entries are an
// error (the error text names the field via the caller's wrapper).
func stringSlice(v any) ([]string, error) {
	items, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("not an array")
	}

	out := make([]string, 0, len(items))

	for _, item := range items {
		s, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("non-string entry")
		}

		out = append(out, s)
	}

	return out, nil
}
