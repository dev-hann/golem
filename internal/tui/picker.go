// Compatibility wrapper: the picker lives in kit; this alias keeps existing
// call sites (the setup wizard) stable.

package tui

import "github.com/dev-hann/golem/internal/tui/kit"

// PickFromList runs the themed picker with the default theme.
func PickFromList(title string, items []string, cursor int) (string, bool) {
	return kit.New(kit.DefaultTheme()).PickFromList(title, items, cursor)
}
