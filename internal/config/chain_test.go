package config_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dev-hann/golem/internal/config"
)

// ADR-0012 precedence: built-in defaults < global file < project file < env.
// Maps deep-merge; slices replace (docs/CONFIG.md Merge rules).

// writeLayerFile seeds a config file at path inside dir and returns its path.
func writeLayerFile(t *testing.T, dir, name, body string) string {
	t.Helper()

	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}

	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}

	return path
}

func TestConfig_PrecedenceFullChain(t *testing.T) {
	// Not parallel: mutates GOLEM_PROVIDER via t.Setenv.
	t.Setenv("GOLEM_PROVIDER", "")

	global := writeLayerFile(t, t.TempDir(), "config.toml", `
[provider]
default = "zai"

[provider.zai]
model = "glm-5"
api_key = "ref:ZAI_API_KEY"

[session]
auto_compact_bytes = 262144
`)

	projDir := t.TempDir()
	project := writeLayerFile(t, projDir, ".golem.toml", `
[provider.zai]
model = "glm-6"

[provider.ollama]
base_url = "http://localhost:11434/v1"
`)

	cfg, err := config.LoadChain(global, project)
	if err != nil {
		t.Fatalf("LoadChain: %v", err)
	}

	if cfg.Default != "zai" {
		t.Errorf("Default = %q, want zai (global file)", cfg.Default)
	}
	if zai := cfg.Providers["zai"]; zai.Model != "glm-6" {
		t.Errorf("zai model = %q, want glm-6 (project overrides global)", zai.Model)
	}
	if zai := cfg.Providers["zai"]; zai.APIKey != "ref:ZAI_API_KEY" {
		t.Errorf("zai api_key = %q, want global value preserved on deep merge", zai.APIKey)
	}
	if ollama := cfg.Providers["ollama"]; ollama.BaseURL != "http://localhost:11434/v1" {
		t.Errorf("ollama base_url = %q, want project-added value", ollama.BaseURL)
	}
	if cfg.Session.AutoCompactBytes != 262144 {
		t.Errorf("auto_compact_bytes = %d, want 262144 (global, untouched by project)", cfg.Session.AutoCompactBytes)
	}
	if cfg.Path != project {
		t.Errorf("Path = %q, want highest existing layer %q", cfg.Path, project)
	}
}

func TestConfig_PrecedenceEnvBeatsFiles(t *testing.T) {
	// Not parallel: mutates GOLEM_PROVIDER via t.Setenv.
	t.Setenv("GOLEM_PROVIDER", "openai")

	global := writeLayerFile(t, t.TempDir(), "config.toml", `[provider]
default = "zai"
`)

	cfg, err := config.LoadChain(global, "")
	if err != nil {
		t.Fatalf("LoadChain: %v", err)
	}

	if cfg.Default != "openai" {
		t.Errorf("Default = %q, want openai (env override)", cfg.Default)
	}
}

func TestConfig_DeepMergeMaps_ReplaceSlices(t *testing.T) {
	t.Parallel()

	global := writeLayerFile(t, t.TempDir(), "config.toml", `
[plugins.mcp.github]
command = ["./github-mcp", "-stdio", "--verbose"]
env = { GITHUB_TOKEN = "ref:GITHUB_TOKEN", API = "global" }
`)

	project := writeLayerFile(t, t.TempDir(), ".golem.toml", `
[plugins.mcp.github]
command = ["./github-mcp"]
env = { API = "project" }
`)

	cfg, err := config.LoadChain(global, project)
	if err != nil {
		t.Fatalf("LoadChain: %v", err)
	}

	gh := cfg.Plugins["github"]
	if got := strings.Join(gh.Command, " "); got != "./github-mcp" {
		t.Errorf("command = %q, want %q (slices replace, never concatenate)", got, "./github-mcp")
	}
	if gh.Env["API"] != "project" {
		t.Errorf("env API = %q, want project (map deep-merge, high layer wins)", gh.Env["API"])
	}
	if gh.Env["GITHUB_TOKEN"] != "ref:GITHUB_TOKEN" {
		t.Errorf("env GITHUB_TOKEN = %q, want global key preserved on merge", gh.Env["GITHUB_TOKEN"])
	}
}

func TestLoadChain_MissingFilesApplyDefaults(t *testing.T) {
	// Not parallel: mutates GOLEM_PROVIDER via t.Setenv.
	t.Setenv("GOLEM_PROVIDER", "")

	absent := filepath.Join(t.TempDir(), "absent.toml")

	cfg, err := config.LoadChain(absent, filepath.Join(t.TempDir(), ".golem.toml"))
	if err != nil {
		t.Fatalf("LoadChain: %v", err)
	}

	if cfg.Default != config.DefaultProviderID {
		t.Errorf("Default = %q, want built-in default", cfg.Default)
	}
	if cfg.Path != "" {
		t.Errorf("Path = %q, want empty when no file existed", cfg.Path)
	}
}

func TestLoadChain_ParseErrorNamesFile(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		globalBody, projectBody string
		wantPath                func(global, project string) string
	}{
		"broken global": {
			globalBody: "[provider\nbroken =",
			wantPath:   func(global, _ string) string { return global },
		},
		"broken project": {
			globalBody:  "[provider]\ndefault = \"zai\"\n",
			projectBody: "[plugins.mcp.x]\ncommand =",
			wantPath:    func(_, project string) string { return project },
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			global := writeLayerFile(t, t.TempDir(), "config.toml", tc.globalBody)

			projDir := t.TempDir()
			project := writeLayerFile(t, projDir, ".golem.toml", tc.projectBody)

			_, err := config.LoadChain(global, project)
			if err == nil || !strings.Contains(err.Error(), tc.wantPath(global, project)) {
				t.Errorf("expected parse error naming the file, got: %v", err)
			}
		})
	}
}

// ADR-0015: the project file is committable, so it never carries plaintext
// secrets. The global file keeps them as the designed default.
func TestLoadChain_ProjectPlaintextAPIKeyRejected(t *testing.T) {
	t.Parallel()

	global := writeLayerFile(t, t.TempDir(), "config.toml", `[provider]
default = "zai"
`)

	project := writeLayerFile(t, t.TempDir(), ".golem.toml", `
[provider.zai]
model = "glm-5"
api_key = "sk-plaintext-in-project"
`)

	_, err := config.LoadChain(global, project)
	if err == nil || !strings.Contains(err.Error(), "zai") {
		t.Errorf("expected error naming provider zai, got: %v", err)
	}
	if !errors.Is(err, config.ErrPlaintextKey) {
		t.Errorf("error must wrap ErrPlaintextKey, got: %v", err)
	}
}

func TestLoadChain_ProjectRefAPIKeyAllowed(t *testing.T) {
	t.Parallel()

	project := writeLayerFile(t, t.TempDir(), ".golem.toml", `
[provider.zai]
model = "glm-5"
api_key = "ref:ZAI_API_KEY"
`)

	cfg, err := config.LoadChain("", project)
	if err != nil {
		t.Fatalf("LoadChain: %v", err)
	}
	if cfg.Providers["zai"].APIKey != "ref:ZAI_API_KEY" {
		t.Errorf("api_key = %q, want ref passthrough", cfg.Providers["zai"].APIKey)
	}
}

func TestLoadChain_GlobalPlaintextAPIKeyAllowed(t *testing.T) {
	t.Parallel()

	global := writeLayerFile(t, t.TempDir(), "config.toml", `
[provider]
default = "zai"

[provider.zai]
model = "glm-5"
api_key = "sk-plaintext-global"
`)

	cfg, err := config.LoadChain(global, filepath.Join(t.TempDir(), "absent.golem.toml"))
	if err != nil {
		t.Fatalf("LoadChain: %v", err)
	}
	if cfg.Providers["zai"].APIKey != "sk-plaintext-global" {
		t.Errorf("api_key = %q, want plaintext accepted in the global file", cfg.Providers["zai"].APIKey)
	}
}

func TestProjectPath_JoinsDotGolemToml(t *testing.T) {
	t.Parallel()

	const root = "/work/app"
	if got := config.ProjectPath(root); got != root+"/"+".golem.toml" {
		t.Errorf("ProjectPath = %q", got)
	}
}
