package kit_test

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/dev-hann/golem/internal/tui/kit"
)

// forcedRenderer builds a renderer with a pinned color profile so style
// output is deterministic regardless of the test environment.
func forcedRenderer(p termenv.Profile) *lipgloss.Renderer {
	r := lipgloss.NewRenderer(nil)
	r.SetColorProfile(p)

	return r
}

func TestDefaultTheme_CoversEveryToken(t *testing.T) {
	t.Parallel()

	theme := kit.DefaultTheme()

	for _, token := range kit.Tokens {
		color, ok := theme.Colors[token]
		if !ok {
			t.Errorf("token %q missing from default theme", token)
		}

		_ = color
	}
}

func TestKit_ComponentOutputPerProfile(t *testing.T) {
	tests := []struct {
		name     string
		profile  termenv.Profile
		wantANSI bool
	}{
		{name: "truecolor renders color", profile: termenv.TrueColor, wantANSI: true},
		{name: "ascii renders plain", profile: termenv.Ascii, wantANSI: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			k := kit.NewWithRenderer(kit.DefaultTheme(), forcedRenderer(tt.profile))

			out := k.ErrLine("boom")
			if !strings.Contains(out, "boom") {
				t.Errorf("message lost: %q", out)
			}

			hasANSI := strings.Contains(out, "\x1b[")
			if hasANSI != tt.wantANSI {
				t.Errorf("profile %v: ANSI=%v, want %v (output %q)", tt.profile, hasANSI, tt.wantANSI, out)
			}
		})
	}
}

func TestThemeFromMap_OverridesTokens(t *testing.T) {
	t.Parallel()

	theme, err := kit.ThemeFromMap("custom", map[kit.Token]string{
		kit.Error: "#ff0000",
	})
	if err != nil {
		t.Fatalf("ThemeFromMap: %v", err)
	}

	if theme.Name != "custom" || theme.Colors[kit.Error].Dark != "#ff0000" {
		t.Errorf("theme = %+v", theme)
	}

	// Unspecified tokens inherit the default theme.
	if theme.Colors[kit.OK].Dark == "" {
		t.Error("unspecified tokens must inherit defaults")
	}
}

func TestThemeFromMap_RejectsInvalidColor(t *testing.T) {
	t.Parallel()

	if _, err := kit.ThemeFromMap("bad", map[kit.Token]string{kit.Accent: "not-a-color"}); err == nil {
		t.Error("invalid color must fail")
	}
}

func TestKit_TokensUseTheirOwnColor(t *testing.T) {
	t.Parallel()

	theme := kit.DefaultTheme()
	theme.Colors[kit.Error] = lipgloss.AdaptiveColor{Light: "1", Dark: "1"}
	theme.Colors[kit.OK] = lipgloss.AdaptiveColor{Light: "2", Dark: "2"}

	k := kit.NewWithRenderer(theme, forcedRenderer(termenv.ANSI))

	if out := k.ErrLine("x"); !strings.Contains(out, "\x1b[31") {
		t.Errorf("error line not red: %q", out)
	}

	if out := k.OKLine("x"); !strings.Contains(out, "\x1b[32") {
		t.Errorf("ok line not green: %q", out)
	}
}

func TestKit_UserBoldAssistantPlain(t *testing.T) {
	t.Parallel()

	k := kit.NewWithRenderer(kit.DefaultTheme(), forcedRenderer(termenv.TrueColor))

	user := k.User("you> ", "hello")
	if !strings.Contains(user, "you> ") || !strings.Contains(user, "hello") {
		t.Errorf("user message mangled: %q", user)
	}

	asst := k.Assistant("golem> ", "hi")
	if !strings.Contains(asst, "golem> ") || !strings.Contains(asst, "hi") {
		t.Errorf("assistant message mangled: %q", asst)
	}
}

func TestKit_BoxRendersRoundedFrame(t *testing.T) {
	t.Parallel()

	k := kit.NewWithRenderer(kit.DefaultTheme(), forcedRenderer(termenv.Ascii))

	boxed := k.Box("hello")
	for _, want := range []string{"╭", "╮", "╰", "╯", "hello"} {
		if !strings.Contains(boxed, want) {
			t.Errorf("box missing %q:\n%s", want, boxed)
		}
	}
}

func TestKit_ModalRendersTitleAndFrame(t *testing.T) {
	t.Parallel()

	k := kit.NewWithRenderer(kit.DefaultTheme(), forcedRenderer(termenv.Ascii))

	modal := k.Modal("keys", "ctrl+n new session\nglm-5.3")
	for _, want := range []string{"╭", "╮", "╰", "╯", "keys", "ctrl+n new session", "glm-5.3"} {
		if !strings.Contains(modal, want) {
			t.Errorf("modal missing %q:\n%s", want, modal)
		}
	}
}

func TestKit_OverlayCentersModal(t *testing.T) {
	t.Parallel()

	k := kit.NewWithRenderer(kit.DefaultTheme(), forcedRenderer(termenv.Ascii))

	placed := k.Overlay(40, 10, "X")

	lines := strings.Split(strings.TrimRight(placed, "\n"), "\n")
	if len(lines) != 10 {
		t.Fatalf("overlay height = %d lines, want 10", len(lines))
	}

	var middle string

	found := false

	for _, line := range lines {
		if strings.Contains(line, "X") {
			middle, found = line, true

			break
		}
	}

	if !found {
		t.Fatalf("modal content missing from overlay:\n%s", placed)
	}

	if strings.Count(middle[:strings.Index(middle, "X")], " ") < 10 {
		t.Errorf("modal not horizontally centered: %q", middle)
	}
}

func TestKit_StatusBarJoinsWithAccent(t *testing.T) {
	t.Parallel()

	k := kit.NewWithRenderer(kit.DefaultTheme(), forcedRenderer(termenv.Ascii))

	bar := k.StatusBar("zai", "glm-5.3", "session 01J…")
	if !strings.Contains(bar, "zai") || !strings.Contains(bar, "glm-5.3") {
		t.Errorf("status bar lost parts: %q", bar)
	}
}

func TestKit_Divider(t *testing.T) {
	t.Parallel()

	k := kit.NewWithRenderer(kit.DefaultTheme(), forcedRenderer(termenv.Ascii))

	if d := k.Divider(10); strings.Count(d, "─") != 10 {
		t.Errorf("divider width wrong: %q", d)
	}
}
