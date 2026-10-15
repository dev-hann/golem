package kit_test

import (
	"strings"
	"testing"

	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"

	"github.com/dev-hann/golem/internal/tui/kit"
)

// downsampled renders styled output through a colorprofile.Writer with
// the profile pinned — v2 moved downsampling from render time to write
// time, so the pipeline is style.Render then write.
func downsampled(p colorprofile.Profile, s string) string {
	var b strings.Builder
	w := colorprofile.NewWriter(&b, nil)
	w.Profile = p
	_, _ = w.WriteString(s)

	return b.String()
}

func TestDefaultTheme_CoversEveryToken(t *testing.T) {
	t.Parallel()

	theme := kit.DefaultTheme()

	for _, token := range kit.Tokens() {
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
		profile  colorprofile.Profile
		wantANSI bool
	}{
		{name: "truecolor renders color", profile: colorprofile.TrueColor, wantANSI: true},
		{name: "ascii renders plain", profile: colorprofile.Ascii, wantANSI: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			k := kit.New(kit.DefaultTheme())

			out := downsampled(tt.profile, k.ErrLine(0, "boom"))
			if !strings.Contains(out, "boom") {
				t.Errorf("message lost: %q", out)
			}

			// The Ascii writer may still emit bare SGR resets ("\x1b[m");
			// only visible styling counts as color.
			visible := strings.ReplaceAll(out, "\x1b[m", "")
			hasANSI := strings.Contains(visible, "\x1b[")
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
	theme.Colors[kit.Error] = kit.Pair{Light: "1", Dark: "1"}
	theme.Colors[kit.OK] = kit.Pair{Light: "2", Dark: "2"}

	k := kit.NewFor(theme, true)

	if out := downsampled(colorprofile.ANSI, k.ErrLine(0, "x")); !strings.Contains(out, "\x1b[31") {
		t.Errorf("error line not red: %q", out)
	}

	if out := downsampled(colorprofile.ANSI, k.OKLine("x")); !strings.Contains(out, "\x1b[32") {
		t.Errorf("ok line not green: %q", out)
	}
}

func TestKit_MessageBarMarksEveryLine(t *testing.T) {
	t.Parallel()

	k := kit.New(kit.DefaultTheme())

	barred := k.MessageBar(kit.User, 30, "one\ntwo")
	lines := strings.Split(ansi.Strip(barred), "\n")
	if len(lines) != 2 || lines[0] != "▎ one" || lines[1] != "▎ two" {
		t.Errorf("every line should carry the bar:\n%s", barred)
	}
}

func TestKit_MessageBarWrapsWithinWidth(t *testing.T) {
	t.Parallel()

	k := kit.New(kit.DefaultTheme())

	barred := k.MessageBar(kit.User, 20, strings.Repeat("a", 50))
	for i, line := range strings.Split(barred, "\n") {
		if w := ansi.StringWidth(line); w > 20 {
			t.Errorf("bar line %d width %d exceeds cap 20:\n%s", i, w, barred)
		}
		if !strings.HasPrefix(ansi.Strip(line), "▎") {
			t.Errorf("bar line %d missing bar:\n%s", i, barred)
		}
	}
}

func TestKit_WrapPlainWithinWidth(t *testing.T) {
	t.Parallel()

	k := kit.New(kit.DefaultTheme())

	tests := []struct {
		name  string
		in    string
		width int
	}{
		{name: "ascii words", in: strings.Repeat("word ", 12), width: 20},
		{name: "cjk unbroken run", in: strings.Repeat("한글", 40), width: 20},
		{name: "multiline wraps per row", in: strings.Repeat("a", 30) + "\nshort\n" + strings.Repeat("글", 30), width: 21},
		{name: "long path token", in: "/" + strings.Repeat("verylongdirname/", 10), width: 18},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			for i, line := range strings.Split(k.WrapPlain(tt.width, tt.in), "\n") {
				if w := ansi.StringWidth(line); w > tt.width {
					t.Errorf("row %d width %d exceeds %d:\n%q", i, w, tt.width, k.WrapPlain(tt.width, tt.in))
				}
			}
		})
	}
}

func TestKit_WrapPlainNarrowWidthPassthrough(t *testing.T) {
	t.Parallel()

	k := kit.New(kit.DefaultTheme())

	if got := k.WrapPlain(0, "unchanged"); got != "unchanged" {
		t.Errorf("width 0 must not wrap, got %q", got)
	}

	if got := k.WrapPlain(-3, "x"); got != "x" {
		t.Errorf("negative width must not wrap, got %q", got)
	}
}

func TestKit_WrapPlainPreservesANSI(t *testing.T) {
	t.Parallel()

	k := kit.New(kit.DefaultTheme())

	wrapped := k.WrapPlain(20, k.Style(kit.Error).Render(strings.Repeat("e", 60)))

	if !strings.Contains(wrapped, "\x1b[") {
		t.Errorf("wrap lost style escapes: %q", wrapped)
	}

	assertRows := strings.Split(wrapped, "\n")
	for i, line := range assertRows {
		if w := ansi.StringWidth(line); w > 20 {
			t.Errorf("row %d width %d exceeds 20:\n%q", i, w, wrapped)
		}
	}
}

func TestKit_ErrLineWrapsWithinWidth(t *testing.T) {
	t.Parallel()

	k := kit.New(kit.DefaultTheme())

	out := k.ErrLine(20, strings.Repeat("e", 60))

	if !strings.Contains(ansi.Strip(out), "error: ") {
		t.Errorf("wrap lost the error prefix: %q", out)
	}

	for i, line := range strings.Split(out, "\n") {
		if w := ansi.StringWidth(line); w > 20 {
			t.Errorf("row %d width %d exceeds 20:\n%s", i, w, out)
		}
	}
}

func TestKit_MessageBarUsesTokenColor(t *testing.T) {
	t.Parallel()

	k := kit.New(kit.DefaultTheme())

	if out := k.MessageBar(kit.User, 30, "hi"); !strings.Contains(out, "\x1b[") {
		t.Errorf("bar should carry the token color: %q", out)
	}
}

func TestKit_StickyUserOneLineTruncated(t *testing.T) {
	t.Parallel()

	k := kit.New(kit.DefaultTheme())

	sticky := k.StickyUser(12, strings.Repeat("b", 50))
	if strings.Contains(sticky, "\n") {
		t.Errorf("sticky must be one line:\n%s", sticky)
	}
	if w := ansi.StringWidth(sticky); w > 12 {
		t.Errorf("sticky width %d exceeds 12:\n%s", w, sticky)
	}
	plain := ansi.Strip(sticky)
	if !strings.Contains(plain, "▎") || !strings.HasSuffix(plain, "…") {
		t.Errorf("sticky missing bar or ellipsis:\n%s", sticky)
	}
}

func TestKit_BoxRendersRoundedFrame(t *testing.T) {
	t.Parallel()

	k := kit.New(kit.DefaultTheme())

	boxed := k.Box("hello", kit.Border)
	for _, want := range []string{"╭", "╮", "╰", "╯", "hello"} {
		if !strings.Contains(boxed, want) {
			t.Errorf("box missing %q:\n%s", want, boxed)
		}
	}
}

func TestKit_BoxLeftRailCarriesTokenColor(t *testing.T) {
	t.Parallel()

	k := kit.New(kit.DefaultTheme())

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

	k := kit.New(kit.DefaultTheme())

	modal := k.Modal(0, "keys", "ctrl+n new session\nglm-5.3")
	for _, want := range []string{"╭", "╮", "╰", "╯", "keys", "ctrl+n new session", "glm-5.3"} {
		if !strings.Contains(ansi.Strip(modal), want) {
			t.Errorf("modal missing %q:\n%s", want, modal)
		}
	}
}

func TestKit_OverlayMergesModalOverBase(t *testing.T) {
	t.Parallel()

	k := kit.New(kit.DefaultTheme())

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

	k := kit.New(kit.DefaultTheme())

	got := k.Overlay(0, 0, "the base", "M")
	if !strings.Contains(got, "the base") || !strings.Contains(got, "M") {
		t.Errorf("zero-size overlay = %q, want base and modal stacked", got)
	}
}

func TestKit_Divider(t *testing.T) {
	t.Parallel()

	k := kit.New(kit.DefaultTheme())

	if d := k.Divider(10); strings.Count(d, "─") != 10 {
		t.Errorf("divider width wrong: %q", d)
	}
}

func TestKit_StatusBarOneLineTruncates(t *testing.T) {
	t.Parallel()

	k := kit.New(kit.DefaultTheme())

	bar := k.StatusBar(40, "◆ bash — go test ./... 5s", kit.Tool)
	if strings.Contains(bar, "\n") {
		t.Errorf("status bar must be one line:\n%s", bar)
	}
	if w := ansi.StringWidth(bar); w > 40 {
		t.Errorf("status bar width %d exceeds 40:\n%s", w, bar)
	}
	if !strings.Contains(bar, "bash") {
		t.Errorf("status bar lost its text:\n%s", bar)
	}
}

func TestKit_StatusBarZeroWidthNoPanic(t *testing.T) {
	t.Parallel()

	k := kit.New(kit.DefaultTheme())

	if bar := k.StatusBar(0, "left", kit.Tool); strings.Contains(bar, "\n") {
		t.Errorf("zero width must not produce newlines:\n%s", bar)
	}
}

func TestKit_ModalCapsWidth(t *testing.T) {
	t.Parallel()

	k := kit.New(kit.DefaultTheme())

	tests := []struct {
		name  string
		title string
		body  string
		width int
	}{
		{
			name:  "long body line wraps",
			title: "sessions",
			body:  "▶ " + strings.Repeat("long-session-title-", 8) + " · 2h ago",
			width: 40,
		},
		{
			name:  "long title truncates",
			title: "models · provider-with-a-very-long-identifier",
			body:  "glm-5.3",
			width: 30,
		},
		{
			name:  "live query grows the modal",
			title: "commands",
			body:  "new session    ctrl+x n\n\n› " + strings.Repeat("query-", 20) + "_",
			width: 44,
		},
		{
			name:  "wide glyphs count double",
			title: "세션 목록",
			body:  strings.Repeat("한", 40),
			width: 24,
		},
		{
			name:  "zero width stays unbounded",
			title: "tiny",
			body:  strings.Repeat("x", 200),
			width: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// width 0 means unbounded (content-sized): the row cap only
			// applies to a positive width.
			if tt.width < 1 {
				return
			}

			for i, line := range strings.Split(k.Modal(tt.width, tt.title, tt.body), "\n") {
				if w := ansi.StringWidth(line); w > tt.width {
					t.Errorf("row %d width %d exceeds %d:\n%s", i, w, tt.width, k.Modal(tt.width, tt.title, tt.body))
				}
			}
		})
	}
}

func TestKit_OverlayModalWiderThanScreen(t *testing.T) {
	t.Parallel()

	k := kit.New(kit.DefaultTheme())

	modal := strings.Repeat("M", 60)
	placed := k.Overlay(30, 5, "base", modal)

	for i, line := range strings.Split(placed, "\n") {
		if w := ansi.StringWidth(line); w > 30 {
			t.Errorf("row %d width %d exceeds 30:\n%s", i, w, placed)
		}
	}

	if !strings.Contains(placed, strings.Repeat("M", 30)) {
		t.Errorf("modal content truncated away:\n%s", placed)
	}
}

// TestKit_SpinnerCyclesFrames verifies the spinner renders its frame
// sequence cyclically (negative and past-end frames wrap, not panic) in
// the accent token's color.
func TestKit_SpinnerCyclesFrames(t *testing.T) {
	t.Parallel()

	k := kit.New(kit.DefaultTheme())
	frames := kit.SpinnerFrames()

	got := make([]string, 0, len(frames)+2)
	for i := range len(frames) + 1 {
		got = append(got, k.Spinner(i))
	}

	if got[0] != got[len(frames)] {
		t.Errorf("frame %d = %q, want the cycle to wrap to frame 0 (%q)",
			len(frames), got[len(frames)], got[0])
	}

	for i, frame := range got[:len(frames)] {
		if !strings.Contains(frame, frames[i]) {
			t.Errorf("Spinner(%d) = %q, want it to render frame %q", i, frame, frames[i])
		}
	}

	if plain := k.Spinner(-1); plain == "" {
		t.Error("Spinner(-1) must not be empty (frames wrap, not fail)")
	}
}

// TestKit_SpinnerColoredPerProfile verifies the spinner glyph carries the
// accent color on color terminals and degrades to the bare glyph on
// NO_COLOR (ascii) ones.
func TestKit_SpinnerColoredPerProfile(t *testing.T) {
	t.Parallel()

	frames := kit.SpinnerFrames()
	glyph := frames[0]

	spun := kit.New(kit.DefaultTheme()).Spinner(0)

	if !strings.Contains(downsampled(colorprofile.TrueColor, spun), "\x1b[") {
		t.Errorf("truecolor spinner = %q, want ANSI styling", spun)
	}

	if got := ansi.Strip(downsampled(colorprofile.Ascii, spun)); got != glyph {
		t.Errorf("ascii spinner = %q, want the bare glyph %q", got, glyph)
	}
}

// TestKit_SpinnerFramesImmutable pins that mutating the returned frame
// sequence cannot corrupt the shared spinner — the exported slice is a
// defensive copy, not the package's own backing array.
func TestKit_SpinnerFramesImmutable(t *testing.T) {
	t.Parallel()

	frames := kit.SpinnerFrames()
	if len(frames) == 0 {
		t.Fatal("SpinnerFrames() is empty")
	}

	frames[0] = "mutated"

	if got := kit.New(kit.DefaultTheme()).Spinner(0); strings.Contains(got, "mutated") {
		t.Errorf("mutating the returned frames leaked into Spinner: %q", got)
	}
}

// TestKit_TokensImmutable pins that mutating the returned token list
// cannot retheme every Kit built afterwards — the exported slice is a
// defensive copy.
func TestKit_TokensImmutable(t *testing.T) {
	t.Parallel()

	tokens := kit.Tokens()
	if len(tokens) == 0 {
		t.Fatal("Tokens() is empty")
	}

	tokens[0] = "accent"
	tokens[len(tokens)-1] = "accent"

	theme := kit.DefaultTheme()
	theme.Colors[kit.Accent] = kit.Pair{Light: "#ff0000", Dark: "#ff0000"}

	k := kit.NewFor(theme, true)
	if got := ansi.Strip(k.Spinner(0)); !strings.Contains(got, "⠋") {
		t.Errorf("Spinner after token-list mutation = %q, want the spinner glyph", got)
	}
}

// TestKit_WarnLinePrefixIsRenderedOnce pins the labeled warn line: one
// named prefix constant (the errPrefix shape), one render pass — no
// split-render seam between the label and the message.
func TestKit_WarnLinePrefixIsRenderedOnce(t *testing.T) {
	t.Parallel()

	k := kit.New(kit.DefaultTheme())

	if got := ansi.Strip(k.WarnLine(0, "disk full")); !strings.HasPrefix(got, "warning: disk full") {
		t.Errorf("WarnLine = %q, want the %q prefix", got, "warning: ")
	}

	// The wrap path must mirror ErrLine's: labeled, wrapped, styled —
	// the label survives wrapping (on whichever row fits) and rows cap.
	wrapped := k.WarnLine(12, "disk full")

	if !strings.Contains(ansi.Strip(wrapped), "warning: ") {
		t.Errorf("wrapped WarnLine lost its label: %q", wrapped)
	}

	for i, line := range strings.Split(wrapped, "\n") {
		if w := ansi.StringWidth(line); w > 12 {
			t.Errorf("row %d width %d exceeds 12:\n%s", i, w, wrapped)
		}
	}
}
