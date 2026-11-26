package testkit

import (
	"testing"

	"github.com/kazupc/paritydesk/internal/i18n"
)

// Lang sets the process and display languages for the rest of the test
// and restores both when it ends.
func Lang(t testing.TB, l i18n.Lang) {
	t.Helper()
	prev, prevDisplay := i18n.SetLang(l), i18n.SetDisplayLang(l)
	t.Cleanup(func() {
		i18n.SetLang(prev)
		i18n.SetDisplayLang(prevDisplay)
	})
}
