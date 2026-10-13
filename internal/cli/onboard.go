// First-run provider onboarding: when no provider is configured, a short
// wizard writes ~/.config/golem/config.toml (ref:-style secrets only, per
// docs/CONFIG.md).

package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/dev-hann/golem/internal/config"
	"github.com/dev-hann/golem/internal/llm"
)

// maxKeyAttempts bounds failed API-key tests before the wizard gives up.
const maxKeyAttempts = 3

// OnboardOptions configures a wizard run.
type OnboardOptions struct {
	ConfigPath string
	Stdin      io.Reader
	Stdout     io.Writer
	Stderr     io.Writer
	// TestKey validates a key against the provider (injected; production
	// wiring performs a minimal live stream).
	TestKey func(providerID, key, model string) error
}

// NeedsOnboarding reports whether the first-run wizard should run: no
// config file exists and no provider key is present in the environment.
func NeedsOnboarding(configPath string) bool {
	if _, err := os.Stat(configPath); err == nil {
		return false
	}

	return len(llm.DetectFromEnv()) == 0
}

// RunOnboard is the setup wizard body: pick a provider, pick a model, prove
// the key works, write the config file. It also backs `golem setup`.
func RunOnboard(opts OnboardOptions) error {
	if opts.Stdin == nil {
		opts.Stdin = os.Stdin
	}

	if opts.Stdout == nil {
		opts.Stdout = os.Stdout
	}

	if opts.Stderr == nil {
		opts.Stderr = os.Stderr
	}

	if opts.TestKey == nil {
		opts.TestKey = liveKeyTest
	}

	out := bufio.NewWriter(opts.Stdout)
	defer func() { _ = out.Flush() }()

	reader := bufio.NewScanner(opts.Stdin)

	id, err := promptProvider(out, reader)
	if err != nil {
		return fmt.Errorf("cli: setup: %w", err)
	}

	preset := llm.Presets[id]

	model, err := promptModel(out, reader, id, preset)
	if err != nil {
		return fmt.Errorf("cli: setup: %w", err)
	}

	if preset.KeyEnv != "" {
		if err := promptAndTestKey(out, reader, opts, id, model); err != nil {
			return err
		}
	}

	settings := config.ProviderSettings{Model: model, BaseURL: preset.BaseURL}
	if preset.KeyEnv != "" {
		settings.APIKey = "ref:" + preset.KeyEnv
	}

	existing, err := config.Load(opts.ConfigPath)
	if err != nil {
		return fmt.Errorf("cli: setup: %w", err)
	}

	providers := existing.Providers
	providers[id] = settings

	if err := writeConfig(opts.ConfigPath, id, providers); err != nil {
		return err
	}

	fmt.Fprintf(out, "\nWrote %s — provider: %s, model: %s\n", //nolint:errcheck // terminal output
		opts.ConfigPath, id, model)

	if existing.Path != "" {
		fmt.Fprintln(out, "A running golem server picks this up on next restart.") //nolint:errcheck // terminal output
	}

	return nil
}

// promptProvider lists the catalog and reads the selection (number or id).
func promptProvider(out *bufio.Writer, reader *bufio.Scanner) (string, error) {
	fmt.Fprintln(out, "golem setup — choose a provider:") //nolint:errcheck // terminal output

	detected := map[string]bool{}
	for _, id := range llm.DetectFromEnv() {
		detected[id] = true
	}

	for i, id := range llm.Order {
		preset := llm.Presets[id]

		note := ""
		if preset.KeyEnv == "" {
			note = "(no key needed)"
		} else if detected[id] {
			note = "(" + preset.KeyEnv + " found)"
		}

		fmt.Fprintf(out, "  %d) %-9s %-28s %s\n", i+1, id, preset.Label, note) //nolint:errcheck // terminal output
	}

	for range maxProviderAttempts {
		choice := strings.TrimSpace(readLine(reader))

		if choice == "" {
			choice = defaultChoice(detected)
		}

		if id, err := resolveChoice(choice); err == nil {
			return id, nil
		}

		fmt.Fprintln(out, "invalid selection — enter 1-4 or a provider id") //nolint:errcheck // terminal output
	}

	return "", errors.New("too many invalid selections")
}

// maxProviderAttempts bounds invalid provider selections.
const maxProviderAttempts = 3

// defaultChoice preselects the first provider whose key is already set.
func defaultChoice(detected map[string]bool) string {
	for _, id := range llm.Order {
		if detected[id] {
			return id
		}
	}

	return llm.Order[0]
}

// resolveChoice maps a number or provider id onto the catalog.
func resolveChoice(choice string) (string, error) {
	if n, err := strconv.Atoi(choice); err == nil {
		if n >= 1 && n <= len(llm.Order) {
			return llm.Order[n-1], nil
		}

		return "", errors.New("out of range")
	}

	if _, ok := llm.Presets[choice]; ok {
		return choice, nil
	}

	return "", errors.New("unknown provider")
}

// promptModel reads the model, offering the preset default.
func promptModel(out *bufio.Writer, reader *bufio.Scanner, id string, preset llm.Preset) (string, error) {
	suggestion := preset.DefaultModel
	if suggestion == "" {
		suggestion = "<required — e.g. llama3.3>"
	}

	fmt.Fprintf(out, "model for %s [%s]: ", id, suggestion) //nolint:errcheck // terminal output

	model := strings.TrimSpace(readLine(reader))
	if model == "" {
		model = preset.DefaultModel
	}

	if model == "" {
		fmt.Fprintln(out, "a model id is required for this provider") //nolint:errcheck // terminal output

		return "", errors.New("missing model")
	}

	return model, nil
}

// promptAndTestKey reads a key (falling back to the preset env var), tests
// it, and retries on failure.
func promptAndTestKey(
	out *bufio.Writer,
	reader *bufio.Scanner,
	opts OnboardOptions,
	id, model string,
) error {
	preset := llm.Presets[id]

	envKey := os.Getenv(preset.KeyEnv)

	for attempt := 1; attempt <= maxKeyAttempts; attempt++ {
		prompt := "API key"
		if envKey != "" {
			prompt += " (blank: use $" + preset.KeyEnv + ")"
		}

		fmt.Fprintf(out, "%s: ", prompt) //nolint:errcheck // terminal output

		key := strings.TrimSpace(readLine(reader))
		if key == "" {
			key = envKey
		}

		if key == "" {
			fmt.Fprintln(out, "no key available — set it in your shell or paste it here") //nolint:errcheck // terminal output

			continue
		}

		fmt.Fprintln(out, "testing key…") //nolint:errcheck // terminal output

		if err := opts.TestKey(id, key, model); err != nil {
			fmt.Fprintf(out, "key test failed: %v\n", err) //nolint:errcheck // terminal output

			continue
		}

		fmt.Fprintln(out, "key works ✓") //nolint:errcheck // terminal output

		if envKey == "" {
			fmt.Fprintf(out, //nolint:errcheck // terminal output
				"\nAdd the key to your shell profile (config stores only a reference):\n  export %s=%s\n",
				preset.KeyEnv, key)
		}

		return nil
	}

	return fmt.Errorf("cli: setup: key test failed %d times", maxKeyAttempts)
}

// readLine consumes one scanner line, returning "" at end of input.
func readLine(reader *bufio.Scanner) string {
	if !reader.Scan() {
		return ""
	}

	return reader.Text()
}

// writeProviderSection renders one [provider.<id>] table.
func writeProviderSection(id string, settings config.ProviderSettings) string {
	var body strings.Builder

	body.WriteString("\n[provider." + id + "]\n")

	if settings.Model != "" {
		body.WriteString("model = \"" + settings.Model + "\"\n")
	}

	if settings.BaseURL != "" {
		body.WriteString("base_url = \"" + settings.BaseURL + "\"\n")
	}

	if settings.APIKey != "" {
		body.WriteString("api_key = \"" + settings.APIKey + "\"\n")
	}

	return body.String()
}

// writeConfig persists the provider section, creating parent directories.
func writeConfig(path, defaultID string, providers map[string]config.ProviderSettings) error {
	var body strings.Builder

	body.WriteString("[provider]\ndefault = \"" + defaultID + "\"\n")

	for _, id := range llm.Order {
		settings, ok := providers[id]
		if !ok {
			continue
		}

		body.WriteString(writeProviderSection(id, settings))
		delete(providers, id)
	}

	// Custom provider ids outside the catalog survive in sorted order.
	extra := make([]string, 0, len(providers))
	for id := range providers {
		extra = append(extra, id)
	}

	sort.Strings(extra)

	for _, id := range extra {
		body.WriteString(writeProviderSection(id, providers[id]))
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("cli: setup: creating config dir: %w", err)
	}

	if err := os.WriteFile(path, []byte(body.String()), 0o600); err != nil {
		return fmt.Errorf("cli: setup: writing %s: %w", path, err)
	}

	return nil
}
