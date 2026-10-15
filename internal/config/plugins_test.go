package config_test

import (
	"strings"
	"testing"

	"github.com/dev-hann/golem/internal/config"
)

// [plugins.mcp.<name>] per docs/CONFIG.md: command (argv, required),
// env (values support ref:), disabled, timeout_secs (default applied by
// the mcp runtime — config stores the raw value, 0 = absent).

func TestLoad_ParsesMCPPlugins(t *testing.T) {
	t.Parallel()

	path := writeConfig(t, `
[plugins.mcp.github]
command = ["./github-mcp", "-stdio"]
env = { GITHUB_TOKEN = "ref:GITHUB_TOKEN" }
timeout_secs = 60

[plugins.mcp.fs]
command = ["npx", "fs-mcp"]
disabled = true
`)

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if len(cfg.Plugins) != 2 {
		t.Fatalf("Plugins = %v, want 2 entries", cfg.Plugins)
	}

	gh := cfg.Plugins["github"]
	if strings.Join(gh.Command, " ") != "./github-mcp -stdio" {
		t.Errorf("github command = %v", gh.Command)
	}
	if gh.Env["GITHUB_TOKEN"] != "ref:GITHUB_TOKEN" {
		t.Errorf("github env = %v", gh.Env)
	}
	if gh.TimeoutSecs != 60 {
		t.Errorf("github timeout_secs = %d, want 60", gh.TimeoutSecs)
	}
	if gh.Disabled {
		t.Error("github disabled = true, want false")
	}

	fs := cfg.Plugins["fs"]
	if !fs.Disabled {
		t.Error("fs disabled = false, want true (parsed but skipped at launch)")
	}
	if fs.TimeoutSecs != 0 {
		t.Errorf("fs timeout_secs = %d, want 0 (absent; runtime default applies)", fs.TimeoutSecs)
	}
}

func TestLoad_PluginsTableOptional(t *testing.T) {
	t.Parallel()

	cfg, err := config.Load(writeConfig(t, `[provider]
default = "zai"
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if len(cfg.Plugins) != 0 {
		t.Errorf("Plugins = %v, want empty when [plugins] absent", cfg.Plugins)
	}
}

func TestLoad_PluginMissingCommandFails(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"command absent": `
[plugins.mcp.broken]
timeout_secs = 30
`,
		"command empty": `
[plugins.mcp.broken]
command = []
`,
	}

	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := config.Load(writeConfig(t, body))
			if err == nil || !strings.Contains(err.Error(), "broken") || !strings.Contains(err.Error(), "command") {
				t.Errorf("expected error naming plugin and field, got: %v", err)
			}
		})
	}
}

func TestLoad_PluginNonStringCommandEntryFails(t *testing.T) {
	t.Parallel()

	_, err := config.Load(writeConfig(t, `
[plugins.mcp.odd]
command = ["./srv", 42]
`))
	if err == nil || !strings.Contains(err.Error(), "odd") {
		t.Errorf("expected error naming plugin odd, got: %v", err)
	}
}

func TestDefaultPluginTimeoutSecs_MatchesConfigDoc(t *testing.T) {
	t.Parallel()

	if config.DefaultPluginTimeoutSecs != 30 {
		t.Errorf("DefaultPluginTimeoutSecs = %d, want 30 (docs/CONFIG.md)", config.DefaultPluginTimeoutSecs)
	}
}
