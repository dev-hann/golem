package cli_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dev-hann/golem/internal/cli"
	"github.com/dev-hann/golem/internal/llm"
)

// errAuth marks a rejected key attempt.
var errAuth = fmt.Errorf("%w: stub says no", llm.ErrAuth)

// onboardHarness wires a wizard run against scripted input and a stubbed
// model-list endpoint.
type onboardHarness struct {
	configPath string
	stdin      *strings.Reader
	stdout     strings.Builder
	stderr     strings.Builder
	listCalls  []string // keys seen by the stub
	authFails  int      // first N calls are rejected
	models     map[string][]string
	unavailIDs map[string]bool // these providers have no listing endpoint
}

// stubModels is the default catalog the stub serves.
func stubModels() map[string][]string {
	return map[string][]string{
		"zai":       {"glm-5.3-air", "glm-4.7", "glm-5", "embedding-3"},
		"anthropic": {"claude-opus-5", "claude-sonnet-4"},
	}
}

// newOnboard prepares a wizard run writing to a temp config path.
func newOnboard(t *testing.T, input string) *onboardHarness {
	t.Helper()

	return &onboardHarness{
		configPath: filepath.Join(t.TempDir(), "config.toml"),
		stdin:      strings.NewReader(input),
		models:     stubModels(),
		unavailIDs: map[string]bool{"openai": true, "ollama": true},
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
		ListModels: func(id, key string) ([]string, error) {
			h.listCalls = append(h.listCalls, id+":"+key)
			if len(h.listCalls) <= h.authFails {
				return nil, errAuth
			}

			if h.unavailIDs[id] {
				return nil, errors.New("404 page not found")
			}

			return h.models[id], nil
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
	h := newOnboard(t, "1\nsk-zai-123\n\n") // zai, key, enter = default glm-5

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
	if len(h.listCalls) != 1 || h.listCalls[0] != "zai:sk-zai-123" {
		t.Errorf("list calls = %v", h.listCalls)
	}
}

func TestRunOnboard_SelectorListsFilteredModels(t *testing.T) {
	h := newOnboard(t, "1\nsk-zai-123\n\n")

	if err := h.run(t); err != nil {
		t.Fatalf("RunOnboard: %v", err)
	}

	out := h.stdout.String()
	for _, want := range []string{"models for zai", "glm-4.7", "glm-5", "glm-5.3-air"} {
		if !strings.Contains(out, want) {
			t.Errorf("selector output missing %q:\n%s", want, out)
		}
	}

	if strings.Contains(out, "embedding-3") {
		t.Errorf("non-chat model leaked into selector:\n%s", out)
	}
}

func TestRunOnboard_SelectorPicksByNumber(t *testing.T) {
	h := newOnboard(t, "1\nsk-zai-123\n1\n") // pick entry 1 = glm-4.7

	if err := h.run(t); err != nil {
		t.Fatalf("RunOnboard: %v", err)
	}

	body, _ := os.ReadFile(h.configPath)
	if !strings.Contains(string(body), `model = "glm-4.7"`) {
		t.Errorf("picked model wrong:\n%s", string(body))
	}
}

func TestRunOnboard_SelectorAcceptsCustomID(t *testing.T) {
	h := newOnboard(t, "1\nsk-zai-123\nglm-5.4-preview\n")

	if err := h.run(t); err != nil {
		t.Fatalf("RunOnboard: %v", err)
	}

	body, _ := os.ReadFile(h.configPath)
	if !strings.Contains(string(body), `model = "glm-5.4-preview"`) {
		t.Errorf("custom model not honored:\n%s", string(body))
	}
}

func TestRunOnboard_AuthFailureRetries(t *testing.T) {
	h := newOnboard(t, "1\nbad-key\ngood-key\n\n")
	h.authFails = 1

	if err := h.run(t); err != nil {
		t.Fatalf("RunOnboard: %v", err)
	}

	if len(h.listCalls) != 2 {
		t.Fatalf("list calls = %v, want 2 attempts", h.listCalls)
	}
	if !strings.Contains(h.stdout.String(), "rejected") {
		t.Errorf("rejection not surfaced:\n%s", h.stdout.String())
	}
}

func TestRunOnboard_AuthFailureThreeTimesErrors(t *testing.T) {
	h := newOnboard(t, "1\nk1\nk2\nk3\n")
	h.authFails = 3

	if err := h.run(t); err == nil || !strings.Contains(err.Error(), "3 times") {
		t.Errorf("expected exhaustion error, got: %v", err)
	}

	if _, statErr := os.Stat(h.configPath); !os.IsNotExist(statErr) {
		t.Error("config must not be written after key exhaustion")
	}
}

func TestRunOnboard_ListUnavailableFallsBackToManual(t *testing.T) {
	h := newOnboard(t, "3\nsk-openai\ngpt-5.2\n") // openai stub = no listing endpoint

	if err := h.run(t); err != nil {
		t.Fatalf("RunOnboard: %v (stdout: %s)", err, h.stdout.String())
	}

	out := h.stdout.String()
	if !strings.Contains(out, "model list unavailable") {
		t.Errorf("fallback not surfaced:\n%s", out)
	}
	if !strings.Contains(out, "key works") {
		t.Errorf("key should be accepted despite missing endpoint:\n%s", out)
	}

	body, _ := os.ReadFile(h.configPath)
	if !strings.Contains(string(body), `model = "gpt-5.2"`) {
		t.Errorf("manual model not honored:\n%s", string(body))
	}
	if !strings.Contains(string(body), `api_key = "ref:OPENAI_API_KEY"`) {
		t.Errorf("key reference missing:\n%s", string(body))
	}
}

func TestRunOnboard_OllamaFallsBackToManualModel(t *testing.T) {
	h := newOnboard(t, "4\nllama3.3\n") // ollama stub = no listing endpoint (not running)

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
	if len(h.listCalls) != 1 || h.listCalls[0] != "ollama:" {
		t.Errorf("list calls = %v, want one keyless ollama attempt", h.listCalls)
	}
}

func TestRunOnboard_PreservesExistingSections(t *testing.T) {
	h := newOnboard(t, "1\nsk-zai-123\n\n")

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
	h := newOnboard(t, "99\nnope\n2\nsk-ant\n\n")

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

	h := newOnboard(t, "1\n\n\n") // select zai, blank key, default model

	if err := h.run(t); err != nil {
		t.Fatalf("RunOnboard: %v", err)
	}

	if len(h.listCalls) != 1 || h.listCalls[0] != "zai:sk-from-env" {
		t.Errorf("list calls = %v, want sk-from-env", h.listCalls)
	}
	if strings.Contains(h.stdout.String(), "export ZAI_API_KEY=") {
		t.Errorf("env-backed key should not print export guidance:\n%s", h.stdout.String())
	}
}

func TestNormalizeModels(t *testing.T) {
	t.Parallel()

	got := cli.NormalizeModels([]string{
		"glm-5", "embedding-3", "GLM-4.7", "glm-5", "", "whisper-1", "tts-1-hd",
		"bge-m3", "glm-5.3-air", "text-embedding-3-large",
	})
	want := []string{"GLM-4.7", "glm-5", "glm-5.3-air"}

	if len(got) != len(want) {
		t.Fatalf("NormalizeModels() = %v, want %v", got, want)
	}

	for i := range want {
		if got[i] != want[i] {
			t.Errorf("NormalizeModels()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
