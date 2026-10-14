package kit_test

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
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

func TestKit_MessageBarMarksEveryLine(t *testing.T) {
	t.Parallel()

	k := kit.NewWithRenderer(kit.DefaultTheme(), forcedRenderer(termenv.Ascii))

	barred := k.MessageBar(kit.User, 30, "one\ntwo")
	lines := strings.Split(barred, "\n")
	if len(lines) != 2 || lines[0] != "▎ one" || lines[1] != "▎ two" {
		t.Errorf("every line should carry the bar:\n%s", barred)
	}
}

func TestKit_MessageBarWrapsWithinWidth(t *testing.T) {
	t.Parallel()

	k := kit.NewWithRenderer(kit.DefaultTheme(), forcedRenderer(termenv.Ascii))

	barred := k.MessageBar(kit.User, 20, strings.Repeat("a", 50))
	for i, line := range strings.Split(barred, "\n") {
		if w := ansi.StringWidth(line); w > 20 {
			t.Errorf("bar line %d width %d exceeds cap 20:\n%s", i, w, barred)
		}
		if !strings.HasPrefix(line, "▎") {
			t.Errorf("bar line %d missing bar:\n%s", i, barred)
		}
	}
}

func TestKit_MessageBarUsesTokenColor(t *testing.T) {
	t.Parallel()

	k := kit.NewWithRenderer(kit.DefaultTheme(), forcedRenderer(termenv.TrueColor))

	if out := k.MessageBar(kit.User, 30, "hi"); !strings.Contains(out, "\x1b[") {
		t.Errorf("bar should carry the token color: %q", out)
	}
}

func TestKit_StickyUserOneLineTruncated(t *testing.T) {
	t.Parallel()

	k := kit.NewWithRenderer(kit.DefaultTheme(), forcedRenderer(termenv.Ascii))

	sticky := k.StickyUser(12, strings.Repeat("b", 50))
	if strings.Contains(sticky, "\n") {
		t.Errorf("sticky must be one line:\n%s", sticky)
	}
	if w := ansi.StringWidth(sticky); w > 12 {
		t.Errorf("sticky width %d exceeds 12:\n%s", w, sticky)
	}
	if !strings.Contains(sticky, "▎") || !strings.HasSuffix(sticky, "…") {
		t.Errorf("sticky missing bar or ellipsis:\n%s", sticky)
	}
}

func TestKit_BoxRendersRoundedFrame(t *testing.T) {
	t.Parallel()

	k := kit.NewWithRenderer(kit.DefaultTheme(), forcedRenderer(termenv.Ascii))

	boxed := k.Box("hello", kit.Border)
	for _, want := range []string{"╭", "╮", "╰", "╯", "hello"} {
		if !strings.Contains(boxed, want) {
			t.Errorf("box missing %q:\n%s", want, boxed)
		}
	}
}

func TestKit_BoxLeftRailCarriesTokenColor(t *testing.T) {
	t.Parallel()

	k := kit.NewWithRenderer(kit.DefaultTheme(), forcedRenderer(termenv.TrueColor))

	// The accent-styled rail glyph is exactly what the left edge must emit.
	rail := k.Style(kit.Accent).Render("│")

	if out := k.Box("hi", kit.Accent); !strings.Contains(out, rail) {
		t.Errorf("left rail missing accent color:\n%q", out)
	}

	if out := k.Box("hi", kit.Border); strings.Contains(out, rail) {
		t.Errorf("plain box leaked the accent color:\n%q", out)
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

func TestKit_OverlayMergesModalOverBase(t *testing.T) {
	t.Parallel()

	k := kit.NewWithRenderer(kit.DefaultTheme(), forcedRenderer(termenv.Ascii))

	base := strings.Repeat("background line\n", 12)
	placed := k.Overlay(40, 12, base, "MODAL")

	lines := strings.Split(strings.TrimRight(placed, "\n"), "\n")
	if len(lines) != 12 {
		t.Fatalf("overlay height = %d lines, want 12", len(lines))
	}

	// The base survives above and below the modal.
	if !strings.Contains(lines[0], "background line") {
		t.Errorf("base missing above modal:\n%s", placed)
	}

	if !strings.Contains(lines[11], "background line") {
		t.Errorf("base missing below modal:\n%s", placed)
	}

	// The modal sits centered with base text on both flanks.
	var modalLine string

	for _, line := range lines {
		if strings.Contains(line, "MODAL") {
			modalLine = line

			break
		}
	}

	if modalLine == "" {
		t.Fatalf("modal missing:\n%s", placed)
	}

	if !strings.Contains(modalLine, "background line") {
		t.Errorf("base flanks missing around modal: %q", modalLine)
	}

	if idx := strings.Index(modalLine, "MODAL"); idx < 10 {
		t.Errorf("modal not centered (left flank %d): %q", idx, modalLine)
	}
}

func TestKit_OverlayZeroSizeFallsBackToBase(t *testing.T) {
	t.Parallel()

	k := kit.NewWithRenderer(kit.DefaultTheme(), forcedRenderer(termenv.Ascii))

	got := k.Overlay(0, 0, "the base", "M")
	if !strings.Contains(got, "the base") || !strings.Contains(got, "M") {
		t.Errorf("zero-size overlay = %q, want base and modal stacked", got)
	}
}

func TestKit_Divider(t *testing.T) {
	t.Parallel()

	k := kit.NewWithRenderer(kit.DefaultTheme(), forcedRenderer(termenv.Ascii))

	if d := k.Divider(10); strings.Count(d, "─") != 10 {
		t.Errorf("divider width wrong: %q", d)
	}
}

func TestKit_StatusBarOneLineFitsWidth(t *testing.T) {
	t.Parallel()

	k := kit.NewWithRenderer(kit.DefaultTheme(), forcedRenderer(termenv.Ascii))

	bar := k.StatusBar(40, "bash — go test ./... 5s", "esc 중단", kit.Tool)
	if strings.Contains(bar, "\n") {
		t.Errorf("status bar must be one line:\n%s", bar)
	}
	if w := ansi.StringWidth(bar); w > 40 {
		t.Errorf("status bar width %d exceeds 40:\n%s", w, bar)
	}
	for _, want := range []string{"bash", "esc"} {
		if !strings.Contains(bar, want) {
			t.Errorf("status bar missing %q:\n%s", want, bar)
		}
	}
}

func TestKit_StatusBarTruncatesLeftSegmentFirst(t *testing.T) {
	t.Parallel()

	k := kit.NewWithRenderer(kit.DefaultTheme(), forcedRenderer(termenv.Ascii))

	bar := k.StatusBar(20, strings.Repeat("l", 40), strings.Repeat("r", 8), kit.Tool)
	if strings.Contains(bar, "\n") {
		t.Errorf("status bar must be one line:\n%s", bar)
	}
	if w := ansi.StringWidth(bar); w > 20 {
		t.Errorf("status bar width %d exceeds 20:\n%s", w, bar)
	}
	if !strings.Contains(bar, strings.Repeat("r", 8)) {
		t.Errorf("right segment should survive, left should yield:\n%s", bar)
	}
}

func TestKit_StatusBarZeroWidthNoPanic(t *testing.T) {
	t.Parallel()

	k := kit.NewWithRenderer(kit.DefaultTheme(), forcedRenderer(termenv.Ascii))

	if bar := k.StatusBar(0, "left", "right", kit.Tool); strings.Contains(bar, "\n") {
		t.Errorf("zero width must not produce newlines:\n%s", bar)
	}
}
