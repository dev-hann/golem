// First-run provider onboarding: when no provider is configured, a short
// wizard writes ~/.config/golem/config.toml (ref:-style secrets only, per
// docs/CONFIG.md). The model choice comes from the provider's live model
// list when the endpoint offers one.

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
	"github.com/dev-hann/golem/internal/tui"
)

// maxKeyAttempts bounds rejected keys before the wizard gives up.
const maxKeyAttempts = 3

// maxModelChoices caps the rendered model list.
const maxModelChoices = 30

// OnboardOptions configures a wizard run.
type OnboardOptions struct {
	ConfigPath string
	Stdin      io.Reader
	Stdout     io.Writer
	Stderr     io.Writer
	// ListModels fetches a provider's model ids and doubles as key
	// validation (injected; production wiring hits the live endpoint).
	ListModels func(providerID, key string) ([]string, error)
}

// NeedsOnboarding reports whether the first-run wizard should run: no
// config file exists and detection does not point at exactly one provider
// — zero keys, or one key shared by several presets (ZAI_API_KEY selects
// both zai plans), which only the wizard can disambiguate.
func NeedsOnboarding(configPath string) bool {
	if _, err := os.Stat(configPath); err == nil {
		return false
	}

	return len(llm.DetectFromEnv()) != 1
}

// RunOnboard is the setup wizard body: pick a provider, prove the key via
// the model-list endpoint, pick a model from the live list, write the
// config file. It also backs `golem setup`.
// Prompts are written unbuffered: they must be on screen before the wizard
// blocks on stdin.
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

	if opts.ListModels == nil {
		opts.ListModels = LiveListModels
	}

	out := opts.Stdout

	reader := bufio.NewScanner(opts.Stdin)

	id, err := promptProvider(out, reader)
	if err != nil {
		return fmt.Errorf("cli: setup: %w", err)
	}

	preset := llm.Presets[id]

	var apiKey string

	models := []string(nil)

	if preset.KeyEnv != "" {
		apiKey, models, err = promptKeyAndList(out, reader, opts, id)
		if err != nil {
			return err
		}
	} else if listed, listErr := opts.ListModels(id, ""); listErr == nil {
		models = listed // keyless local provider answered with models
	}

	model, err := promptModel(out, reader, id, preset, models)
	if err != nil {
		return fmt.Errorf("cli: setup: %w", err)
	}

	settings := config.ProviderSettings{Model: model, BaseURL: preset.BaseURL}
	if preset.KeyEnv != "" {
		settings.APIKey = apiKey
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

	if Probe(DefaultSocketPath()) {
		fmt.Fprintln(out, //nolint:errcheck // terminal output
			"warning: a golem server is already running and predates this config — restart it:")
		fmt.Fprintln(out, "  pkill -f 'golem serve'   # then run: golem") //nolint:errcheck // terminal output
	} else if existing.Path != "" {
		fmt.Fprintln(out, "A running golem server picks this up on next restart.") //nolint:errcheck // terminal output
	}

	return nil
}

// promptProvider lists the catalog and reads the selection (number or id).
func promptProvider(out io.Writer, reader *bufio.Scanner) (string, error) {
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

// promptKeyAndList reads a key (blank falls back to the preset env var) and
// validates it by listing models: a rejected credential retries, a missing
// listing endpoint accepts the key and returns no models. It returns the
// working key (stored as plaintext per ADR-0015) alongside the models.
func promptKeyAndList(
	out io.Writer,
	reader *bufio.Scanner,
	opts OnboardOptions,
	id string,
) (key string, models []string, err error) {
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

		fmt.Fprintln(out, "fetching models…") //nolint:errcheck // terminal output

		models, err := opts.ListModels(id, key)
		switch {
		case err == nil:
			fmt.Fprintln(out, "key works ✓ (stored in config, chmod 600)") //nolint:errcheck // terminal output

			return key, models, nil
		case errors.Is(err, llm.ErrAuth):
			fmt.Fprintf(out, "key rejected: %v\n", err) //nolint:errcheck // terminal output

			if hint := planHint(id); hint != "" {
				fmt.Fprintln(out, "hint: "+hint) //nolint:errcheck // terminal output
			}

			continue
		default:
			// The key may be fine; only the listing endpoint is missing.
			fmt.Fprintf(out, "key accepted (model list unavailable: %v)\n", err) //nolint:errcheck // terminal output
			fmt.Fprintln(out, "key stored in config, chmod 600")                 //nolint:errcheck // terminal output

			return key, nil, nil
		}
	}

	return "", nil, fmt.Errorf("cli: setup: key rejected %d times", maxKeyAttempts)
}

// planHint points at the sibling zai preset when a key fits the other
// plan: coding-plan keys only work on the coding endpoint and vice versa.
func planHint(id string) string {
	switch id {
	case "zai":
		return "a GLM Coding Plan key only works on zai-coding — rerun: golem setup"
	case "zai-coding":
		return "a pay-per-use key only works on zai — rerun: golem setup"
	default:
		return ""
	}
}

// promptModel picks the model: a numbered selector over the fetched list,
// or manual entry with the preset default as the suggestion.
func promptModel(
	out io.Writer,
	reader *bufio.Scanner,
	id string,
	preset llm.Preset,
	models []string,
) (string, error) {
	if len(models) > 0 {
		return pickModelFromList(out, reader, id, NormalizeModels(models), preset.DefaultModel)
	}

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

// pickModelFromList renders the selector: on a terminal it is an arrow-key
// picker (bubbletea, no new dependency); without one it falls back to
// numbered input. Cancel (q) drops to the numbered prompt so custom ids can
// still be typed.
func pickModelFromList(
	out io.Writer,
	reader *bufio.Scanner,
	id string,
	models []string,
	defaultModel string,
) (string, error) {
	listed := models
	if len(listed) > maxModelChoices {
		listed = listed[:maxModelChoices]
	}

	cursor := cursorFor(listed, defaultModel)

	if item, ok := tui.PickFromList(titleFor(id, listed), listed, cursor); ok {
		return item, nil
	}

	fmt.Fprintf(out, "\n%s\n", titleFor(id, listed)) //nolint:errcheck // terminal output

	for i, m := range listed {
		marker := "  "
		if i == cursor {
			marker = "▶ "
		}

		fmt.Fprintf(out, "%s%2d) %s\n", marker, i+1, m) //nolint:errcheck // terminal output
	}

	fmt.Fprintf(out, "pick 1-%d, type an id, or enter for %q: ", //nolint:errcheck // terminal output
		len(listed), listed[cursor])

	choice := strings.TrimSpace(readLine(reader))

	switch {
	case choice == "":
		return listed[cursor], nil
	case isIndex(choice, len(listed)):
		n, _ := strconv.Atoi(choice)

		return listed[n-1], nil
	default:
		return choice, nil
	}
}

// titleFor renders the picker title.
func titleFor(id string, listed []string) string {
	return fmt.Sprintf("models for %s (%d) — ↑↓ move, enter select, q to type an id", id, len(listed))
}

// cursorFor prepositions the cursor on the preset default.
func cursorFor(listed []string, defaultModel string) int {
	if defaultModel != "" {
		for i, m := range listed {
			if m == defaultModel {
				return i
			}
		}
	}

	return 0
}

// isIndex reports whether s is a valid 1-based index into n items.
func isIndex(s string, n int) bool {
	num, err := strconv.Atoi(s)

	return err == nil && num >= 1 && num <= n
}

// nonChatFragments mark model ids that cannot serve chat completions.
var nonChatFragments = []string{
	"embedding", "embed-", "bge-", "reranker", "whisper", "tts",
	"dall-e", "-image", "moderation", "transcribe", "realtime", "-audio",
}

// NormalizeModels dedupes, filters non-chat ids, and sorts the list.
func NormalizeModels(ids []string) []string {
	seen := map[string]bool{}

	filtered := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}

		if hasAnyFold(id, nonChatFragments) {
			continue
		}

		seen[id] = true
		filtered = append(filtered, id)
	}

	sort.Strings(filtered)

	return filtered
}

// hasAnyFold reports whether s contains any fragment, case-insensitively.
func hasAnyFold(s string, fragments []string) bool {
	lower := strings.ToLower(s)
	for _, fragment := range fragments {
		if strings.Contains(lower, fragment) {
			return true
		}
	}

	return false
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
