package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/dev-hann/golem/pkg/golemclient"
)

// welcomeModel builds a connected, resized model with no session — the
// state a fresh launch shows before its first message (ADR-0018).
func welcomeModel(t *testing.T) Model {
	t.Helper()

	m, _ := wired(t)

	return resize(m, 80, 24)
}

// welcomeRow returns the index of the first row carrying the banner's
// top edge, or -1 when absent.
func welcomeRow(lines []string) int {
	for i, line := range lines {
		if strings.Contains(ansi.Strip(line), "____") {
			return i
		}
	}

	return -1
}

// welcomeRowsFit asserts the screen's exact-height invariant: every row
// within the terminal width, the row count exactly the terminal height.
func welcomeRowsFit(t *testing.T, m Model) {
	t.Helper()

	view := m.View()

	if got := strings.Count(view, "\n") + 1; got != m.height {
		t.Errorf("screen height = %d rows, want exactly %d", got, m.height)
	}

	for i, line := range strings.Split(view, "\n") {
		if w := ansi.StringWidth(ansi.Strip(line)); w > m.width {
			t.Errorf("row %d cells wide, exceeds terminal %d: %q", i, m.width, line)
		}
	}
}

func TestWelcome_ShownOnFreshLaunch(t *testing.T) {
	t.Parallel()

	m := welcomeModel(t)

	if !m.showWelcome() {
		t.Fatal("showWelcome() = false on a fresh no-session launch, want true")
	}

	if row := welcomeRow(strings.Split(m.View(), "\n")); row < 0 {
		t.Errorf("view missing the golem banner:\n%s", m.View())
	}
}

func TestWelcome_BannerGeometryPinned(t *testing.T) {
	t.Parallel()

	if len(welcomeArt) != welcomeArtRows {
		t.Fatalf("welcomeArt has %d rows, want %d", len(welcomeArt), welcomeArtRows)
	}

	for i, row := range welcomeArt {
		if got := ansi.StringWidth(row); got != welcomeArtWidth {
			t.Errorf("banner row %d width = %d, want the uniform %d: %q", i, got, welcomeArtWidth, row)
		}
	}
}

func TestWelcome_ContainsProjectNameAndInfo(t *testing.T) {
	t.Parallel()

	m := welcomeModel(t)
	view := ansi.Strip(m.View())

	for _, want := range []string{
		"golem",
		"message to start",
		"ctrl+p",
		"ctrl+x",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("welcome screen missing %q:\n%s", want, view)
		}
	}
}

func TestWelcome_HiddenAfterFirstMessage(t *testing.T) {
	t.Parallel()

	m := welcomeModel(t)

	// Typing alone keeps the welcome; submitting removes it.
	if m2 := key(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hello")}); !m2.showWelcome() {
		t.Error("typing must not hide the welcome screen before submit")
	}

	m = key(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hello")})
	m = key(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	if m.showWelcome() {
		t.Error("welcome still shown after the first submitted message")
	}

	if row := welcomeRow(strings.Split(m.View(), "\n")); row >= 0 {
		t.Errorf("banner still rendered after first submit:\n%s", m.View())
	}
}

func TestWelcome_HiddenWhenSessionLoaded(t *testing.T) {
	t.Parallel()

	m := welcomeModel(t)

	m = deliver(t, m, golemclient.SessionMeta{
		Type: golemclient.TypeSessionMeta, Session: "01JTESTSESSIONID000000000",
	})

	if m.showWelcome() {
		t.Error("welcome still shown after session_meta named a session")
	}

	if row := welcomeRow(strings.Split(m.View(), "\n")); row >= 0 {
		t.Errorf("banner still rendered after a session loaded:\n%s", m.View())
	}
}

func TestWelcome_HiddenWithPendingPreConnect(t *testing.T) {
	t.Parallel()

	m := New("/dev/null")
	m.dial = func(string) (client, error) { return newFakeClient(), nil }
	m = resize(m, 80, 24)

	m = key(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hi")})
	m = key(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	if m.showWelcome() {
		t.Error("welcome shown while a pre-connect submit pends")
	}
}

func TestWelcome_LayoutIntact(t *testing.T) {
	t.Parallel()

	m := welcomeModel(t)

	welcomeRowsFit(t, m)
}

func TestWelcome_Centered(t *testing.T) {
	t.Parallel()

	m := welcomeModel(t)
	lines := strings.Split(m.View(), "\n")

	row := welcomeRow(lines)
	if row < 0 {
		t.Fatalf("no banner row found:\n%s", m.View())
	}

	// The banner's top row must sit exactly at the centered inset: the
	// screen pad, then the viewport-center inset, then the art row.
	center := max((m.vp.Width-welcomeArtWidth)/2, 0)
	want := strings.Repeat(" ", screenPadH+center) + welcomeArt[0]

	if got := ansi.Strip(lines[row]); got != want {
		t.Errorf("banner row = %q, want the centered %q", got, want)
	}
}

func TestWelcome_VerticallyCentered(t *testing.T) {
	t.Parallel()

	m := welcomeModel(t)
	lines := strings.Split(m.View(), "\n")

	row := welcomeRow(lines)
	if row < 0 {
		t.Fatalf("no banner row found:\n%s", m.View())
	}

	// The transcript region starts below the header (when shown) and the
	// screen's top padding; the banner must sit inside it with breathing
	// room, not flush against its top edge.
	top := screenPadV + headerRowsIfShown(m.height)

	spaceAbove := row - top
	if spaceAbove < 1 {
		t.Errorf("banner starts flush at the transcript top (row %d): expected vertical centering", row)
	}
}

func TestWelcome_TinyTerminalFitsWidth(t *testing.T) {
	t.Parallel()

	m, _ := wired(t)
	m = resize(m, 30, 10)

	welcomeRowsFit(t, m)
}
