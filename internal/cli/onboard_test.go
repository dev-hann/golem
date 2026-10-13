package cli_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dev-hann/golem/internal/cli"
)

// errTestKey marks a rejected key attempt.
var errTestKey = errors.New("key rejected by provider")

// onboardHarness wires a wizard run against scripted input.
type onboardHarness struct {
	configPath string
	stdin      *strings.Reader
	stdout     strings.Builder
	stderr     strings.Builder
	keyCalls   []string
	failFirst  int
}

// newOnboard prepares a wizard run writing to a temp config path.
func newOnboard(t *testing.T, input string) *onboardHarness {
	t.Helper()

	return &onboardHarness{
		configPath: filepath.Join(t.TempDir(), "config.toml"),
		stdin:      strings.NewReader(input),
	}
}

// run executes the wizard, returning its error.
func (h *onboardHarness) run(t *testing.T) error {
	t.Helper()

	return cli.RunOnboard(cli.OnboardOptions{
		ConfigPath: h.configPath,
		Stdin:      h.stdin,
		Stdout:     &h.stdout,
		Stderr:     &h.stderr,
		TestKey: func(_, key, _ string) error {
			h.keyCalls = append(h.keyCalls, key)
			if len(h.keyCalls) <= h.failFirst {
				return errTestKey
			}

			return nil
		},
	})
}

func TestNeedsOnboarding(t *testing.T) {
	t.Setenv("ZAI_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")

	dir := t.TempDir()
	absent := filepath.Join(dir, "absent.toml")
	if !cli.NeedsOnboarding(absent) {
		t.Error("NeedsOnboarding(no config, no keys) = false, want true")
	}

	existing := filepath.Join(dir, "existing.toml")
	if err := os.WriteFile(existing, []byte("[provider]\ndefault = \"zai\"\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if cli.NeedsOnboarding(existing) {
		t.Error("NeedsOnboarding(config exists) = true, want false")
	}

	t.Setenv("ZAI_API_KEY", "sk-set")
	if cli.NeedsOnboarding(absent) {
		t.Error("NeedsOnboarding(env key set) = true, want false")
	}
}

func TestRunOnboard_WritesRefStyleConfig(t *testing.T) {
	h := newOnboard(t, "1\n\nsk-zai-123\n")

	if err := h.run(t); err != nil {
		t.Fatalf("RunOnboard: %v (stderr: %s)", err, h.stderr.String())
	}

	body, err := os.ReadFile(h.configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}

	got := string(body)
	for _, want := range []string{
		`default = "zai"`,
		`[provider.zai]`,
		`model = "glm-5"`,
		`base_url = "https://api.z.ai/api/paas/v4/"`,
		`api_key = "ref:ZAI_API_KEY"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("config missing %q:\n%s", want, got)
		}
	}

	if strings.Contains(got, "sk-zai-123") {
		t.Errorf("config leaks the plaintext key:\n%s", got)
	}
	if !strings.Contains(h.stdout.String(), "export ZAI_API_KEY=") {
		t.Errorf("stdout missing export guidance:\n%s", h.stdout.String())
	}
	if len(h.keyCalls) != 1 || h.keyCalls[0] != "sk-zai-123" {
		t.Errorf("key test calls = %v", h.keyCalls)
	}
}

func TestRunOnboard_KeyTestFailureRetries(t *testing.T) {
	h := newOnboard(t, "1\n\nbad-key\ngood-key\n")
	h.failFirst = 1

	if err := h.run(t); err != nil {
		t.Fatalf("RunOnboard: %v", err)
	}

	if len(h.keyCalls) != 2 {
		t.Fatalf("key test calls = %v, want 2 attempts", h.keyCalls)
	}
}

func TestRunOnboard_OllamaNeedsNoKey(t *testing.T) {
	h := newOnboard(t, "4\nllama3.3\n")

	if err := h.run(t); err != nil {
		t.Fatalf("RunOnboard: %v", err)
	}

	body, _ := os.ReadFile(h.configPath)
	got := string(body)

	if !strings.Contains(got, `default = "ollama"`) || !strings.Contains(got, `model = "llama3.3"`) {
		t.Errorf("ollama config wrong:\n%s", got)
	}
	if strings.Contains(got, "api_key") {
		t.Errorf("ollama config should have no api_key:\n%s", got)
	}
	if len(h.keyCalls) != 0 {
		t.Errorf("key test calls = %v, want none", h.keyCalls)
	}
}

func TestRunOnboard_PreservesExistingSections(t *testing.T) {
	h := newOnboard(t, "1\n\nsk-zai-123\n")

	existing := "[provider]\ndefault = \"openai\"\n\n[provider.openai]\nmodel = \"gpt-5.2\"\n"
	if err := os.WriteFile(h.configPath, []byte(existing), 0o600); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	if err := h.run(t); err != nil {
		t.Fatalf("RunOnboard: %v", err)
	}

	body, _ := os.ReadFile(h.configPath)
	got := string(body)

	if !strings.Contains(got, `default = "zai"`) {
		t.Errorf("default not switched:\n%s", got)
	}
	if !strings.Contains(got, `[provider.openai]`) || !strings.Contains(got, `model = "gpt-5.2"`) {
		t.Errorf("existing openai section lost:\n%s", got)
	}
}

func TestRunOnboard_InvalidSelectionRecovers(t *testing.T) {
	h := newOnboard(t, "99\nnope\n2\nclaude-opus-5\nsk-ant\n")

	if err := h.run(t); err != nil {
		t.Fatalf("RunOnboard: %v", err)
	}

	body, _ := os.ReadFile(h.configPath)
	if !strings.Contains(string(body), `default = "anthropic"`) {
		t.Errorf("anthropic not selected:\n%s", string(body))
	}
}

func TestRunOnboard_UsesEnvKeyWhenBlank(t *testing.T) {
	t.Setenv("ZAI_API_KEY", "sk-from-env")

	h := newOnboard(t, "1\n\n\n") // select zai, default model, blank key

	if err := h.run(t); err != nil {
		t.Fatalf("RunOnboard: %v", err)
	}

	if len(h.keyCalls) != 1 || h.keyCalls[0] != "sk-from-env" {
		t.Errorf("key test calls = %v, want sk-from-env", h.keyCalls)
	}
}
